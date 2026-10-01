package primer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/primer"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/store"
)

type fakePinger struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *fakePinger) Ping(context.Context, domain.PrimerSettings) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "ok", p.err
}

func (p *fakePinger) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type harness struct {
	db       *store.DB
	service  *primer.Service
	pinger   *fakePinger
	now      time.Time
	snapshot *decision.UsageSnapshot
	notified []domain.NotificationEvent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{db: db, pinger: &fakePinger{}, now: time.Date(2026, 9, 29, 11, 0, 0, 0, time.UTC)}
	latest := func(context.Context, string) (decision.UsageSnapshot, error) {
		if h.snapshot == nil {
			return decision.UsageSnapshot{}, store.ErrNotFound
		}
		return *h.snapshot, nil
	}
	h.service = &primer.Service{
		Store: db, Pinger: h.pinger, Providers: map[string]string{"claude-main": "claude", "codex-main": "codex"},
		Latest: latest, Refresh: latest, Now: func() time.Time { return h.now },
		Notify: func(_ context.Context, event domain.NotificationEvent) { h.notified = append(h.notified, event) },
	}
	return h
}

func (h *harness) observe(resetsAt *time.Time) {
	snapshot := decision.UsageSnapshot{Provider: "claude", ObservedAt: h.now, Source: "native",
		Weekly: decision.UsageWindow{Remaining: .8, ResetsAt: h.now.Add(72 * time.Hour)}}
	if resetsAt != nil {
		snapshot.Short = &decision.UsageWindow{Remaining: 1, ResetsAt: *resetsAt}
	}
	h.snapshot = &snapshot
}

func (h *harness) save(t *testing.T, settings domain.PrimerSettings) {
	t.Helper()
	if _, err := h.service.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) attempts(t *testing.T) []domain.PrimerAttempt {
	t.Helper()
	attempts, err := h.db.ListPrimerAttempts(context.Background(), "claude-main", 50)
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

func scheduleUTC(times ...string) domain.PrimerSettings {
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Times, settings.Timezone = true, times, "UTC"
	return settings
}

func TestScheduledSlotFiresOnceEvenAcrossRestarts(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	h.save(t, scheduleUTC("06:00"))
	h.observe(nil)
	h.now = time.Date(2026, 9, 29, 6, 1, 5, 0, time.UTC)
	h.service.Tick(context.Background())
	h.service.Tick(context.Background())
	// A new service over the same database stands in for a restart.
	restarted := &primer.Service{Store: h.db, Pinger: h.pinger, Providers: h.service.Providers,
		Latest: h.service.Latest, Refresh: h.service.Refresh, Now: h.service.Now}
	restarted.Tick(context.Background())
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d, want exactly one", h.pinger.count())
	}
	attempts := h.attempts(t)
	if len(attempts) != 1 || attempts[0].Outcome != domain.PrimerFired || attempts[0].Verification != domain.PrimerVerifyPending {
		t.Fatalf("attempts=%#v", attempts)
	}
}

func TestPingIsSkippedWhileAWindowIsOpenOrARunIsActive(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	h.save(t, scheduleUTC("06:00", "08:00"))
	h.now = time.Date(2026, 9, 29, 6, 1, 0, 0, time.UTC)
	open := h.now.Add(2 * time.Hour)
	h.observe(&open)
	h.service.Tick(context.Background())
	attempts := h.attempts(t)
	if h.pinger.count() != 0 || len(attempts) != 1 || attempts[0].Outcome != domain.PrimerSkipped ||
		!strings.Contains(attempts[0].Reason, "already open") {
		t.Fatalf("pings=%d attempts=%#v", h.pinger.count(), attempts)
	}

	h.observe(nil)
	createActiveRun(t, h.db, h.now)
	h.now = time.Date(2026, 9, 29, 8, 1, 0, 0, time.UTC)
	h.service.Tick(context.Background())
	attempts = h.attempts(t)
	if h.pinger.count() != 0 || !strings.Contains(attempts[0].Reason, "run is active") {
		t.Fatalf("pings=%d attempts=%#v", h.pinger.count(), attempts)
	}
}

func createActiveRun(t *testing.T, db *store.DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{ID: "p", ProviderAccountID: "claude-main",
		HarnessType: "claude-code", WorkspaceProvider: "existing-directory", Repository: "/tmp"}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, domain.Task{ID: "task", Name: "task", ExecutionProfileID: "p", Type: domain.OneOff}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(ctx, "run-1", "task", "claude-main", "", now); err != nil {
		t.Fatal(err)
	}
}

func TestMissedSlotsAreRecordedAfterSleep(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	h.save(t, scheduleUTC("06:00"))
	h.service.Tick(context.Background())
	h.now = time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	h.service.Tick(context.Background())
	attempts := h.attempts(t)
	if h.pinger.count() != 0 || len(attempts) != 1 || attempts[0].Outcome != domain.PrimerSkipped ||
		!strings.HasPrefix(attempts[0].Reason, "Missed") {
		t.Fatalf("pings=%d attempts=%#v", h.pinger.count(), attempts)
	}
}

func TestResetModeChainsWindowsAndVerifiesTheNewOne(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	reset := time.Date(2026, 9, 29, 11, 20, 0, 253_000_000, time.UTC)
	h.observe(&reset)
	h.now = reset.Add(30 * time.Second)
	h.service.Tick(context.Background())
	if h.pinger.count() != 0 {
		t.Fatal("reset mode must wait for the fire delay after the reset")
	}
	h.now = reset.Add(time.Minute + time.Second)
	h.observe(nil)
	h.service.Tick(context.Background())
	h.service.Tick(context.Background())
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d, want one", h.pinger.count())
	}
	pingAt := h.now
	h.now = pingAt.Add(2 * time.Minute)
	newReset := time.Date(2026, 9, 29, 16, 20, 0, 0, time.UTC)
	h.observe(&newReset)
	h.service.Tick(context.Background())
	attempts := h.attempts(t)
	if attempts[0].Verification != domain.PrimerVerified || attempts[0].WindowResetsAt == nil ||
		!attempts[0].WindowResetsAt.Equal(newReset) {
		t.Fatalf("attempt=%#v", attempts[0])
	}
}

func TestResetModeDoesNotRepingWhileItsSampleStillPredatesThePing(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	reset := time.Date(2026, 9, 29, 11, 20, 0, 0, time.UTC)
	h.observe(&reset)
	h.now = reset.Add(time.Minute + time.Second)
	// The only sample (taken by the skip check) shows no window, and every
	// later refresh fails, as while the usage endpoint rate limits.
	h.observe(nil)
	stale := *h.snapshot
	h.service.Refresh = func(context.Context, string) (decision.UsageSnapshot, error) {
		return decision.UsageSnapshot{}, errors.New("429")
	}
	h.service.Latest = func(context.Context, string) (decision.UsageSnapshot, error) { return stale, nil }
	for step := 0; step < 12; step++ {
		h.service.Tick(context.Background())
		h.now = h.now.Add(15 * time.Minute)
	}
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d; the window our own ping opened must suppress idle retries", h.pinger.count())
	}
}

func TestResetModeBacksOffAndNotifiesOnceWhenPingsKeepFailing(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	h.observe(nil)
	h.pinger.err = errors.New("claude exited with code 1: signed out")
	start := h.now
	for h.now.Before(start.Add(6 * time.Hour)) {
		h.observe(nil)
		h.service.Tick(context.Background())
		h.now = h.now.Add(primer.TickInterval)
	}
	// Immediate, +15m, +30m, +1h, +2h, +4h: six attempts in six hours
	// rather than twenty-four.
	if calls := h.pinger.count(); calls < 4 || calls > 7 {
		t.Fatalf("pings=%d, want exponential backoff", calls)
	}
	if len(h.notified) != 1 {
		t.Fatalf("notifications=%d, want one per run of failures", len(h.notified))
	}
}

func TestZeroCatchUpStillFiresOnTheNextTick(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	settings := scheduleUTC("06:00")
	settings.CatchUpSeconds = 0
	h.save(t, settings)
	h.observe(nil)
	h.now = time.Date(2026, 9, 29, 6, 1, 17, 0, time.UTC)
	h.service.Tick(context.Background())
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d; a zero catch-up must not turn every slot into a miss", h.pinger.count())
	}
}

func TestVerificationWaitsForALateSampleBeforeGivingUp(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	if _, err := h.service.PingNow(context.Background(), "claude-main", false); err != nil {
		t.Fatal(err)
	}
	pingAt := h.now
	h.now = pingAt.Add(2 * time.Minute)
	h.observe(nil)
	h.service.Tick(context.Background())
	if got := h.attempts(t)[0]; got.Verification != domain.PrimerVerifyPending {
		t.Fatalf("an early empty sample must not settle verification: %#v", got)
	}
	h.now = pingAt.Add(10 * time.Minute)
	opened := pingAt.Add(primer.WindowLength)
	h.observe(&opened)
	h.service.Tick(context.Background())
	if got := h.attempts(t)[0]; got.Verification != domain.PrimerVerified {
		t.Fatalf("attempt=%#v", got)
	}
}

func TestVerificationRefreshesWhenTheStoredSampleShowsNoWindow(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	if _, err := h.service.PingNow(context.Background(), "claude-main", false); err != nil {
		t.Fatal(err)
	}
	pingAt := h.now
	h.now = pingAt.Add(3 * time.Minute)
	h.observe(nil)
	stored := *h.snapshot
	opened := pingAt.Add(primer.WindowLength)
	h.service.Latest = func(context.Context, string) (decision.UsageSnapshot, error) { return stored, nil }
	h.service.Refresh = func(context.Context, string) (decision.UsageSnapshot, error) {
		fresh := stored
		fresh.ObservedAt = h.now
		fresh.Short = &decision.UsageWindow{Remaining: 1, ResetsAt: opened}
		return fresh, nil
	}
	h.service.Tick(context.Background())
	if got := h.attempts(t)[0]; got.Verification != domain.PrimerVerified {
		t.Fatalf("attempt=%#v", got)
	}
}

func TestResetModeRetriesAfterAnUnverifiedPingWithoutNewSamples(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	h.observe(nil)
	h.service.Tick(context.Background())
	pingAt := h.now
	// One sample after the ping settles, then nothing new: the usage
	// monitor is off and every refresh is rate limited.
	h.now = pingAt.Add(2 * time.Minute)
	h.observe(nil)
	stored := *h.snapshot
	h.service.Latest = func(context.Context, string) (decision.UsageSnapshot, error) { return stored, nil }
	h.service.Refresh = func(context.Context, string) (decision.UsageSnapshot, error) {
		return decision.UsageSnapshot{}, errors.New("429")
	}
	for h.now.Before(pingAt.Add(time.Hour)) {
		h.service.Tick(context.Background())
		h.now = h.now.Add(primer.TickInterval)
	}
	if h.pinger.count() < 2 {
		t.Fatalf("pings=%d; an unverified ping must not suppress retries for five hours", h.pinger.count())
	}
}

func TestPingsThatOpenNoWindowBackOffAndNotifyOnce(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	// Every sample, before and after each ping, shows no window: the local
	// Claude login is not the monitored account, so pings never register.
	start := h.now
	for h.now.Before(start.Add(8 * time.Hour)) {
		h.observe(nil)
		h.service.Tick(context.Background())
		h.now = h.now.Add(primer.TickInterval)
	}
	// Each ping waits ~2 minutes for its verdict, then backs off 15m, 30m,
	// 1h, 2h: five pings in eight hours. Without backoff it would be about
	// thirty.
	if calls := h.pinger.count(); calls < 4 || calls > 6 {
		t.Fatalf("pings=%d, want unverified pings to back off like failures", calls)
	}
	if len(h.notified) != 1 {
		t.Fatalf("notifications=%d, want one for the run of unverified pings", len(h.notified))
	}
}

func TestSkippedResetSlotFallsBackToIdleRetriesInStatusAndTick(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	// A window resets at 11:10; a run is active when the reset slot fires,
	// so the slot is skipped.
	reset := h.now.Add(10 * time.Minute)
	h.observe(&reset)
	createActiveRun(t, h.db, h.now)
	h.now = reset.Add(primer.FireDelay)
	h.service.Tick(context.Background())
	attempts := h.attempts(t)
	if len(attempts) != 1 || attempts[0].Outcome != domain.PrimerSkipped || attempts[0].TargetAt.Sub(reset) != 0 {
		t.Fatalf("attempts=%#v", attempts)
	}
	h.now = h.now.Add(primer.TickInterval)
	status, err := h.service.Status(context.Background(), "claude-main")
	if err != nil || status.NextPingAt == nil || status.NextPingAt.Before(h.now) {
		t.Fatalf("status must not promise a ping in the past: next=%v err=%v", status.NextPingAt, err)
	}
	// Once the run ends, the idle retry promised by status actually fires.
	if err := h.db.CompleteRun(context.Background(), "run-1", domain.RunCompletion{State: domain.RunCompleted}, h.now); err != nil {
		t.Fatal(err)
	}
	h.now = *status.NextPingAt
	h.observe(nil)
	h.service.Tick(context.Background())
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d; the idle retry status promised must fire", h.pinger.count())
	}
}

func TestPingCancelledByShutdownIsNotAFailure(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	h.pinger.err = fmt.Errorf("ping cancelled: %w", context.Canceled)
	attempt, err := h.service.PingNow(context.Background(), "claude-main", false)
	if err != nil || !attempt.Interrupted() {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
	if len(h.notified) != 0 {
		t.Fatalf("a shutdown must not notify a failure: %#v", h.notified)
	}
	if run := primer.ConsecutiveFailures(h.attempts(t)); run.Count != 0 {
		t.Fatalf("run=%+v, want shutdown ignored", run)
	}
}

func TestMixedFailureRunNotifiesExactlyOnce(t *testing.T) {
	// A ping that opens no window followed by a hard failure, and the
	// reverse: each run must notify once, whichever kind comes first.
	for _, hardFirst := range []bool{false, true} {
		h := newHarness(t)
		h.observe(nil)
		noWindowPing := func() {
			h.pinger.err = nil
			if _, err := h.service.PingNow(context.Background(), "claude-main", true); err != nil {
				t.Fatal(err)
			}
			h.now = h.now.Add(2 * time.Minute)
			h.observe(nil)
			h.service.Tick(context.Background()) // verification: no window opened
		}
		hardFailure := func() {
			h.pinger.err = errors.New("claude exited with code 1: signed out")
			if _, err := h.service.PingNow(context.Background(), "claude-main", true); err != nil {
				t.Fatal(err)
			}
			h.now = h.now.Add(time.Minute)
		}
		if hardFirst {
			hardFailure()
			if len(h.notified) != 1 {
				t.Fatalf("hardFirst: a hard failure must notify at once: %d", len(h.notified))
			}
			noWindowPing()
			hardFailure()
		} else {
			noWindowPing()
			if len(h.notified) != 0 {
				t.Fatalf("one ping that opened no window is not yet alarming: %d", len(h.notified))
			}
			hardFailure()
			noWindowPing()
		}
		if len(h.notified) != 1 {
			t.Fatalf("hardFirst=%v notifications=%d, want exactly one per run", hardFirst, len(h.notified))
		}
	}
}

func TestPendingPingHoldsItsWindowUntilVerified(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	h.observe(nil)
	h.service.Tick(context.Background())
	pingAt := h.now
	// Samples after the ping keep showing no window, but every refresh is
	// rate limited so verification waits for a later one: no second ping
	// goes out while the first is still being checked.
	h.service.Refresh = func(context.Context, string) (decision.UsageSnapshot, error) {
		return decision.UsageSnapshot{}, errors.New("429")
	}
	for h.now.Before(pingAt.Add(29 * time.Minute)) {
		h.now = h.now.Add(primer.TickInterval)
		h.observe(nil)
		h.service.Tick(context.Background())
	}
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d; a ping still being verified must hold its slot", h.pinger.count())
	}
	status, err := h.service.Status(context.Background(), "claude-main")
	if err != nil || status.NextPingAt == nil || status.NextPingAt.Before(h.now) {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestUnconfirmedWindowIsMarkedUnverified(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	if _, err := h.service.PingNow(context.Background(), "claude-main", false); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(31 * time.Minute)
	h.observe(nil)
	h.service.Tick(context.Background())
	if got := h.attempts(t)[0]; got.Verification != domain.PrimerVerifyUnverified || !strings.Contains(got.Reason, "no open window") {
		t.Fatalf("attempt=%#v", got)
	}
}

func TestForcedPingInsideAnOpenWindowIsExplained(t *testing.T) {
	h := newHarness(t)
	open := h.now.Add(3 * time.Hour)
	h.observe(&open)
	if _, err := h.service.PingNow(context.Background(), "claude-main", true); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(2 * time.Minute)
	h.observe(&open)
	h.service.Tick(context.Background())
	if got := h.attempts(t)[0]; got.Verification != domain.PrimerVerifyUnverified || !strings.Contains(got.Reason, "already open") {
		t.Fatalf("attempt=%#v", got)
	}
}

func TestManualPingRespectsOpenWindowUnlessForced(t *testing.T) {
	h := newHarness(t)
	open := h.now.Add(time.Hour)
	h.observe(&open)
	attempt, err := h.service.PingNow(context.Background(), "claude-main", false)
	if err != nil || attempt.Outcome != domain.PrimerSkipped || h.pinger.count() != 0 {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
	h.now = h.now.Add(time.Second)
	attempt, err = h.service.PingNow(context.Background(), "claude-main", true)
	if err != nil || attempt.Outcome != domain.PrimerFired || h.pinger.count() != 1 {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
	if _, err := h.service.PingNow(context.Background(), "codex-main", true); err == nil {
		t.Fatal("codex is not supported yet")
	}
}

func TestFailedPingIsRecordedAndNotified(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	h.pinger.err = errors.New("claude exited with code 1: signed out")
	attempt, err := h.service.PingNow(context.Background(), "claude-main", false)
	if err != nil || attempt.Outcome != domain.PrimerFailed || !strings.Contains(attempt.Reason, "signed out") {
		t.Fatalf("attempt=%#v err=%v", attempt, err)
	}
	if len(h.notified) != 1 || h.notified[0].Type != domain.EventPrimerFailed {
		t.Fatalf("notified=%#v", h.notified)
	}
}

func TestSavingASchedulePastItsTimeWaitsForTheNextOccurrence(t *testing.T) {
	h := newHarness(t)
	h.observe(nil)
	// It is 11:00; the operator adds 10:30, which is inside the catch-up.
	h.save(t, scheduleUTC("10:30"))
	h.service.Tick(context.Background())
	if h.pinger.count() != 0 {
		t.Fatalf("pings=%d; a slot that passed before the save must not fire", h.pinger.count())
	}
	status, err := h.service.Status(context.Background(), "claude-main")
	if err != nil || status.NextPingAt == nil || status.NextPingAt.Day() != 30 {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestStatusAndTickAgreeOnResetModeTiming(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	h.observe(nil)
	h.service.Tick(context.Background())
	status, err := h.service.Status(context.Background(), "claude-main")
	if err != nil {
		t.Fatal(err)
	}
	// Our own ping opened a window ending five hours out; status must show
	// the next ping just after it and forecast from there.
	wantFire := h.now.Add(primer.WindowLength + primer.FireDelay)
	if status.NextPingAt == nil || !status.NextPingAt.Equal(wantFire) || len(status.Forecast) == 0 ||
		!status.Forecast[0].Start.Equal(h.now.Add(primer.WindowLength)) {
		t.Fatalf("status=%#v", status)
	}
	h.now = h.now.Add(primer.WindowLength + primer.FireDelay)
	h.service.Tick(context.Background())
	if h.pinger.count() != 2 {
		t.Fatalf("pings=%d, want the chained ping at the time status promised", h.pinger.count())
	}
}

func TestInterruptedAttemptsDoNotCountAsFailures(t *testing.T) {
	h := newHarness(t)
	settings := primer.Defaults("claude-main")
	settings.Enabled, settings.Mode = true, domain.PrimerReset
	h.save(t, settings)
	h.observe(nil)
	if _, err := h.db.ClaimPrimerSlot(context.Background(), domain.PrimerAttempt{
		ProviderAccountID: "claude-main", SlotKey: "manual:old", Trigger: "manual", Outcome: domain.PrimerRunning,
		TargetAt: h.now.Add(-time.Hour), StartedAt: h.now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.service.Tick(context.Background())
	if h.pinger.count() != 1 {
		t.Fatalf("pings=%d; an interrupted attempt must not trigger failure backoff", h.pinger.count())
	}
}

func TestSeveralClaudeAccountsAreUnsupported(t *testing.T) {
	h := newHarness(t)
	h.service.Providers["claude-work"] = "claude"
	settings := primer.Defaults("claude-main")
	settings.Enabled = true
	if _, err := h.service.Save(context.Background(), settings); !errors.Is(err, primer.ErrUnsupported) {
		t.Fatalf("err=%v, want ErrUnsupported", err)
	}
	status, err := h.service.Status(context.Background(), "claude-main")
	if err != nil || status.Supported || !strings.Contains(status.UnsupportedReason, "share") {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if _, err := h.service.PingNow(context.Background(), "claude-main", true); !errors.Is(err, primer.ErrUnsupported) {
		t.Fatalf("err=%v, want ErrUnsupported", err)
	}
}

func TestUpdateAppliesPartialChangesAtomically(t *testing.T) {
	h := newHarness(t)
	h.save(t, scheduleUTC("06:00"))
	var wg sync.WaitGroup
	for _, change := range []func(*domain.PrimerSettings){
		func(s *domain.PrimerSettings) { s.Mode = domain.PrimerReset },
		func(s *domain.PrimerSettings) { s.Prompt = "hello" },
		func(s *domain.PrimerSettings) { s.Timezone = "America/Chicago" },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.service.Update(context.Background(), "claude-main", change); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	settings, _, err := h.service.Settings(context.Background(), "claude-main")
	if err != nil || settings.Mode != domain.PrimerReset || settings.Prompt != "hello" || settings.Timezone != "America/Chicago" {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
}

func TestDisabledPrimerNeverPings(t *testing.T) {
	h := newHarness(t)
	settings := scheduleUTC("11:00")
	settings.Enabled = false
	h.save(t, settings)
	h.now = time.Date(2026, 9, 29, 11, 2, 0, 0, time.UTC)
	h.service.Tick(context.Background())
	if h.pinger.count() != 0 || len(h.attempts(t)) != 0 {
		t.Fatal("a disabled primer must do nothing")
	}
}

type failingRunner struct{ calls int }

func (r *failingRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.calls++
	_, _ = command.Stderr.Write([]byte("error: unknown option '--safe-mode'"))
	return 1, nil
}

func TestClaudePingerFailsClosedWithoutSafeMode(t *testing.T) {
	runner := &failingRunner{}
	_, err := primer.ClaudePinger{Runner: runner, Environ: func() []string { return nil }}.Ping(context.Background(), primer.Defaults("claude-main"))
	if err == nil || !strings.Contains(err.Error(), "update Claude Code") || runner.calls != 1 {
		t.Fatalf("err=%v calls=%d; must not retry without --safe-mode", err, runner.calls)
	}
}

type recordingRunner struct{ command redprocess.Command }

func (r *recordingRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	_, _ = command.Stdout.Write([]byte("ok\n"))
	return 0, nil
}

func TestClaudePingerUsesSubscriptionLoginAndMinimalSession(t *testing.T) {
	runner := &recordingRunner{}
	pinger := primer.ClaudePinger{Runner: runner, Environ: func() []string {
		return []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=fake-key", "anthropic_auth_token=x", "ANTHROPIC_BEDROCK_BASE_URL=http://proxy", "CLAUDE_CODE_USE_FOUNDRY=1", "ANTHROPIC_CUSTOM_HEADERS=x", "HOME=/Users/me"}
	}}
	output, err := pinger.Ping(context.Background(), primer.Defaults("claude-main"))
	if err != nil || output != "ok" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	environment := strings.Join(runner.command.Env, "\n")
	if strings.Contains(strings.ToUpper(environment), "ANTHROPIC") || strings.Contains(environment, "CLAUDE_CODE_USE") ||
		!strings.Contains(environment, "HOME=/Users/me") {
		t.Fatalf("environment was not scrubbed: %v", runner.command.Env)
	}
	if !runner.command.KillGroup || runner.command.WaitDelay == 0 {
		t.Fatal("a stuck ping must be killed with its children and not hold the primer")
	}
	args := strings.Join(runner.command.Args, " ")
	for _, want := range []string{"--print", "--model=haiku", "--no-session-persistence", "--safe-mode", "--strict-mcp-config"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "--bare") {
		t.Error("--bare forces API-key auth and must not be used")
	}
	if runner.command.Dir == "" || strings.Contains(args, primer.DefaultPrompt) {
		t.Errorf("prompt must go on stdin from a scratch directory: dir=%q args=%q", runner.command.Dir, args)
	}
}
