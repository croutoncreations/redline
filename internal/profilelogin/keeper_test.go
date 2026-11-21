package profilelogin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/nativeusage"
)

var start = time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)

// fakeProfile stands in for Claude Code and its keychain item: renewing in
// a mode that "works" moves the expiry 8 hours on.
type fakeProfile struct {
	mu        sync.Mutex
	state     nativeusage.ProfileLogin
	renews    map[Mode]bool // modes that renew
	calls     []Mode
	runErr    error
	readErr   error
	signOutOn Mode // a renewal in this mode finds the login revoked
	block     chan struct{}
}

func (f *fakeProfile) ProfileLoginState(context.Context, string) (nativeusage.ProfileLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.readErr
}

func (f *fakeProfile) Renew(_ context.Context, _ string, mode Mode) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mode)
	if mode == f.signOutOn {
		f.state = nativeusage.ProfileLogin{SignedOut: true}
		return errors.New("not logged in")
	}
	if f.renews[mode] {
		f.state.ExpiresAt = f.state.ExpiresAt.Add(8 * time.Hour)
	}
	return f.runErr
}

func (f *fakeProfile) modes() []Mode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Mode(nil), f.calls...)
}

func profile(expiresIn time.Duration, renews ...Mode) *fakeProfile {
	f := &fakeProfile{state: nativeusage.ProfileLogin{ExpiresAt: start.Add(expiresIn), HasRefreshToken: true}, renews: map[Mode]bool{}}
	for _, mode := range renews {
		f.renews[mode] = true
	}
	return f
}

func keeper(f *fakeProfile, now *time.Time) *Keeper {
	return &Keeper{Logins: f, Renewer: f, Now: func() time.Time { return *now }}
}

func TestALoginFarFromExpiryIsLeftAlone(t *testing.T) {
	now := start
	f := profile(2*time.Hour, ModeStatus, ModePrompt)
	if got := keeper(f, &now).Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeFresh || len(f.modes()) != 0 {
		t.Fatalf("result = %+v, calls = %v", got, f.modes())
	}
}

// Inside the lead, auth status is tried first; when it renews, nothing
// that spends quota runs.
func TestAuthStatusRenewsWithoutAPrompt(t *testing.T) {
	now := start
	f := profile(10*time.Minute, ModeStatus, ModePrompt)
	got := keeper(f, &now).Check(context.Background(), "a", "/d", false)
	if got.Outcome != OutcomeRenewed || got.Mode != ModeStatus || !got.After.Equal(start.Add(10*time.Minute+8*time.Hour)) {
		t.Fatalf("result = %+v", got)
	}
	if calls := f.modes(); len(calls) != 1 || calls[0] != ModeStatus {
		t.Fatalf("calls = %v", calls)
	}
}

// Early in the lead, a status that does not renew is not a failure and
// never escalates to the prompt: the next cycle is closer to expiry.
func TestEarlyStatusThatDoesNotRenewWaitsRatherThanPrompting(t *testing.T) {
	now := start
	f := profile(12*time.Minute, ModePrompt)
	k := keeper(f, &now)
	if got := k.Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeWaiting {
		t.Fatalf("result = %+v", got)
	}
	if calls := f.modes(); len(calls) != 1 || calls[0] != ModeStatus {
		t.Fatalf("calls = %v", calls)
	}
	// No backoff: the next cycle tries again, and inside the last five
	// minutes the prompt renews it.
	now = start.Add(8 * time.Minute)
	if got := k.Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeRenewed || got.Mode != ModePrompt {
		t.Fatalf("result = %+v, calls = %v", got, f.modes())
	}
}

// An expired token reported by the collector (force) may use the prompt at
// once, whatever the clock says.
func TestForcedRenewalFallsBackToThePrompt(t *testing.T) {
	now := start
	f := profile(-2*time.Minute, ModePrompt)
	got := keeper(f, &now).Check(context.Background(), "a", "/d", true)
	if got.Outcome != OutcomeRenewed || got.Mode != ModePrompt {
		t.Fatalf("result = %+v", got)
	}
	if calls := f.modes(); len(calls) != 2 || calls[0] != ModeStatus || calls[1] != ModePrompt {
		t.Fatalf("calls = %v", calls)
	}
}

// Success is the expiry moving forward, never the exit code. The first
// failures retry after one monitor cycle; later ones back off longer.
func TestExitCodeAloneIsNotSuccess(t *testing.T) {
	now := start
	f := profile(2 * time.Minute) // nothing renews
	k := keeper(f, &now)
	got := k.Check(context.Background(), "a", "/d", false)
	if got.Outcome != OutcomeFailed || got.Err == nil || got.Failures != 1 || got.NeedsLogin() {
		t.Fatalf("result = %+v", got)
	}
	now = now.Add(time.Minute)
	if got := k.Check(context.Background(), "a", "/d", true); got.Outcome != OutcomeSkipped || len(f.modes()) != 2 {
		t.Fatalf("inside the quick retry: %+v, calls = %v", got, f.modes())
	}
	now = now.Add(QuickRetry)
	f.renews[ModeStatus] = true
	if got := k.Check(context.Background(), "a", "/d", true); got.Outcome != OutcomeRenewed {
		t.Fatalf("after the quick retry: %+v", got)
	}
}

// Renewals that keep failing escalate to "needs login" and slow down.
func TestRepeatedFailuresNeedALoginAndBackOff(t *testing.T) {
	now := start
	f := profile(-time.Minute) // expired, nothing renews
	k := keeper(f, &now)
	var got Result
	for i := 1; i <= NeedsLoginAfter; i++ {
		got = k.Check(context.Background(), "a", "/d", true)
		if got.Failures != i || got.NeedsLogin() != (i >= NeedsLoginAfter) {
			t.Fatalf("attempt %d: %+v", i, got)
		}
		now = now.Add(QuickRetry)
	}
	// After the quick retries, the wait is the full backoff.
	if got := k.Check(context.Background(), "a", "/d", true); got.Outcome != OutcomeSkipped {
		t.Fatalf("during long backoff: %+v", got)
	}
	now = now.Add(DefaultBackoff)
	if got := k.Check(context.Background(), "a", "/d", true); got.Outcome != OutcomeFailed {
		t.Fatalf("after long backoff: %+v", got)
	}
}

// Whatever happens, renewal never spends more than a few prompts a day on
// a seat.
func TestPromptsAreCappedPerDay(t *testing.T) {
	now := start
	f := profile(-time.Minute)
	k := keeper(f, &now)
	k.Backoff = time.Minute
	for range 20 {
		k.Check(context.Background(), "a", "/d", true)
		now = now.Add(31 * time.Minute)
	}
	prompts := 0
	for _, mode := range f.modes() {
		if mode == ModePrompt {
			prompts++
		}
	}
	if prompts != MaxPromptsPerDay {
		t.Fatalf("prompts in ~10h = %d, want %d", prompts, MaxPromptsPerDay)
	}
	now = now.Add(24 * time.Hour)
	k.Check(context.Background(), "a", "/d", true)
	if last := f.modes()[len(f.modes())-1]; last != ModePrompt {
		t.Fatalf("a day later the budget is back; last call = %v", last)
	}
}

// A login without a refresh token keeps working until it is due; only
// then is it a sign-in problem.
func TestNoRefreshTokenIsFineUntilDue(t *testing.T) {
	now := start
	f := &fakeProfile{state: nativeusage.ProfileLogin{ExpiresAt: start.Add(3 * time.Hour)}}
	if got := keeper(f, &now).Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeFresh || got.NeedsLogin() {
		t.Fatalf("result = %+v", got)
	}
}

func TestSignedOutProfilesAreNeverRun(t *testing.T) {
	now := start
	for name, state := range map[string]nativeusage.ProfileLogin{
		"signed out":            {SignedOut: true},
		"no refresh token, due": {ExpiresAt: start.Add(time.Minute)},
	} {
		f := &fakeProfile{state: state}
		if got := keeper(f, &now).Check(context.Background(), "a", "/d", true); got.Outcome != OutcomeSignedOut || len(f.modes()) != 0 {
			t.Fatalf("%s: %+v, calls = %v", name, got, f.modes())
		}
	}
}

// A renewal that finds the login revoked reports sign-out, not failure.
func TestRevokedDuringRenewalIsSignedOut(t *testing.T) {
	now := start
	f := profile(time.Minute)
	f.signOutOn = ModeStatus
	if got := keeper(f, &now).Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeSignedOut {
		t.Fatalf("result = %+v", got)
	}
}

// A Redline run on the account renews the login itself.
func TestABusyAccountIsSkipped(t *testing.T) {
	now := start
	f := profile(time.Minute, ModeStatus)
	k := keeper(f, &now)
	k.Busy = func(context.Context, string) bool { return true }
	if got := k.Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeSkipped || len(f.modes()) != 0 {
		t.Fatalf("result = %+v", got)
	}
}

func TestAnUnreadableLoginIsSkippedWithItsError(t *testing.T) {
	now := start
	f := profile(time.Minute, ModeStatus)
	f.readErr = errors.New("keychain locked")
	if got := keeper(f, &now).Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeSkipped || got.Err == nil || len(f.modes()) != 0 {
		t.Fatalf("result = %+v", got)
	}
}

// profiles routes each directory to its own fake.
type profiles map[string]*fakeProfile

func (p profiles) ProfileLoginState(ctx context.Context, dir string) (nativeusage.ProfileLogin, error) {
	return p[dir].ProfileLoginState(ctx, dir)
}
func (p profiles) Renew(ctx context.Context, dir string, mode Mode) error {
	return p[dir].Renew(ctx, dir, mode)
}

// Only one Claude Code renewal runs at a time, across all profiles.
func TestOneRenewalAtATime(t *testing.T) {
	now := start
	a, b := profile(time.Minute, ModeStatus), profile(time.Minute, ModeStatus)
	a.block = make(chan struct{})
	both := profiles{"/a": a, "/b": b}
	k := &Keeper{Logins: both, Renewer: both, Now: func() time.Time { return now }}
	done := make(chan Result)
	go func() { done <- k.Check(context.Background(), "a", "/a", false) }()
	for k.renewing.TryLock() { // until the first renewal holds the lock
		k.renewing.Unlock()
		time.Sleep(time.Millisecond)
	}
	if got := k.Check(context.Background(), "b", "/b", false); got.Outcome != OutcomeSkipped || len(b.modes()) != 0 {
		t.Fatalf("second renewal = %+v", got)
	}
	close(a.block)
	if got := <-done; got.Outcome != OutcomeRenewed {
		t.Fatalf("first renewal = %+v", got)
	}
	// Free again: b renews on its next check.
	if got := k.Check(context.Background(), "b", "/b", false); got.Outcome != OutcomeRenewed {
		t.Fatalf("b afterwards = %+v", got)
	}
}

// Failures only count in a row: a login that turns out fine (renewed by
// the person's own session) clears them, so a later single failure does
// not report a sign-in problem.
func TestAFreshLoginClearsEarlierFailures(t *testing.T) {
	now := start
	f := profile(-time.Minute)
	k := keeper(f, &now)
	for range NeedsLoginAfter - 1 {
		k.Check(context.Background(), "a", "/d", true)
		now = now.Add(QuickRetry)
	}
	f.mu.Lock()
	f.state.ExpiresAt = now.Add(8 * time.Hour) // their own Claude Code renewed it
	f.mu.Unlock()
	if got := k.Check(context.Background(), "a", "/d", false); got.Outcome != OutcomeFresh {
		t.Fatalf("result = %+v", got)
	}
	now = now.Add(8 * time.Hour)
	if got := k.Check(context.Background(), "a", "/d", true); got.Failures != 1 || got.NeedsLogin() {
		t.Fatalf("a later failure = %+v", got)
	}
}

// A quick retry lands on the next 5-minute cycle even when the renewal
// itself took a while.
func TestQuickRetryIsTheNextCycle(t *testing.T) {
	now := start
	f := profile(-time.Minute)
	slow := &slowRenewer{fakeProfile: f, now: &now, takes: 2 * time.Minute}
	k := &Keeper{Logins: f, Renewer: slow, Now: func() time.Time { return now }}
	cycle := now
	k.Check(context.Background(), "a", "/d", true)
	now = cycle.Add(QuickRetry)
	if got := k.Check(context.Background(), "a", "/d", true); got.Outcome == OutcomeSkipped {
		t.Fatalf("the next cycle was skipped: %+v", got)
	}
}

type slowRenewer struct {
	*fakeProfile
	now   *time.Time
	takes time.Duration
}

func (s *slowRenewer) Renew(ctx context.Context, dir string, mode Mode) error {
	*s.now = s.now.Add(s.takes)
	return s.fakeProfile.Renew(ctx, dir, mode)
}
