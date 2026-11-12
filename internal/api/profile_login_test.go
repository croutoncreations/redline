package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/profilelogin"
	"github.com/croutoncreations/redline/internal/usage"
)

// seat is one fake Claude Code profile: its login expiry, and whether
// Claude Code renews it when run.
type seat struct {
	mu      sync.Mutex
	login   nativeusage.ProfileLogin
	renews  bool
	renewed []profilelogin.Mode
}

type seats map[string]*seat

func (s seats) ProfileLoginState(_ context.Context, dir string) (nativeusage.ProfileLogin, error) {
	p := s[dir]
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.login, nil
}

func (s seats) Renew(_ context.Context, dir string, mode profilelogin.Mode) error {
	p := s[dir]
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renewed = append(p.renewed, mode)
	if p.renews {
		p.login.ExpiresAt = p.login.ExpiresAt.Add(8 * time.Hour)
	}
	return nil
}

// expiringSource fails like the native collector while the seat's token is
// inside its last five minutes, and reads normally once renewed.
type expiringSource struct {
	seats seats
	now   func() time.Time
	reads *int
}

func (expiringSource) Name() string { return "native" }
func (e expiringSource) Fetch(_ context.Context, p config.Provider) (decision.UsageSnapshot, []byte, error) {
	*e.reads++
	dir := config.NormalizeClaudeConfigDir(p.ClaudeConfigDir)
	if _, ok := e.seats[dir]; ok {
		login, _ := e.seats.ProfileLoginState(context.Background(), dir)
		if login.ExpiresAt.Sub(e.now()) <= 5*time.Minute {
			return decision.UsageSnapshot{}, nil, nativeusage.ProfileNeedsSession(dir)
		}
	}
	return decision.UsageSnapshot{Provider: "claude", Source: "native", ObservedAt: e.now(),
		Weekly: decision.UsageWindow{Remaining: .5, ResetsAt: e.now().Add(48 * time.Hour)}}, nil, nil
}

func keepAliveServer(t *testing.T, expiresIn time.Duration, renews bool) (*Server, *seat, *redemptionNotifier, *int, *time.Time) {
	t.Helper()
	s, _, _ := redemptionServer(t, "claude")
	now := s.now()
	s.now = func() time.Time { return now }
	dir := "/p/.claude-cc-frank"
	s.config.Providers = map[string]config.Provider{
		"frank":  {Provider: "claude", UsageSource: "native", ClaudeConfigDir: dir, Label: "Frank"},
		"main":   {Provider: "claude", UsageSource: "native"},
		"codex1": {Provider: "codex", UsageSource: "native"},
	}
	frank := &seat{login: nativeusage.ProfileLogin{ExpiresAt: now.Add(expiresIn), HasRefreshToken: true}, renews: renews}
	all := seats{dir: frank}
	reads := 0
	source := expiringSource{seats: all, now: func() time.Time { return now }, reads: &reads}
	s.usageSources = usage.NewManager(source, source, s.now)
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	s.profileLogins = newProfileLogins(s.config, all, all, nil, func() time.Time { return now })
	return s, frank, notifier, &reads, &now
}

// The monitor renews a seat inside the lead before it fetches, so the
// fetch never sees the token expired and nothing goes stale.
func TestMonitorRenewsAProfileLoginBeforeItLapses(t *testing.T) {
	s, frank, _, reads, _ := keepAliveServer(t, 10*time.Minute, true)
	if err := s.monitorProvider(context.Background(), "frank"); err != nil {
		t.Fatal(err)
	}
	if len(frank.renewed) != 1 || frank.renewed[0] != profilelogin.ModeStatus || *reads != 1 {
		t.Fatalf("renewed = %v, reads = %d", frank.renewed, *reads)
	}
}

// A seat that lapsed between cycles is renewed when the fetch reports it,
// and read again in the same cycle.
func TestAnExpiredProfileIsRenewedAndReadInTheSameCycle(t *testing.T) {
	s, frank, _, reads, now := keepAliveServer(t, 2*time.Hour, true)
	*now = now.Add(2*time.Hour - time.Minute) // a minute before expiry
	s.profileLogins.keeper.Lead = time.Second // the pre-fetch check misses it
	if err := s.monitorProvider(context.Background(), "frank"); err != nil {
		t.Fatalf("monitor: %v (renewed %v)", err, frank.renewed)
	}
	if *reads != 2 || len(frank.renewed) == 0 {
		t.Fatalf("reads = %d, renewed = %v", *reads, frank.renewed)
	}
}

// The default login, other providers, and a disabled setting are never
// touched.
func TestOnlyProfileAccountsAreKeptAlive(t *testing.T) {
	s, frank, _, _, _ := keepAliveServer(t, time.Minute, true)
	for _, id := range []string{"main", "codex1"} {
		if got := s.keepProfileLoginFresh(context.Background(), id, true); got.Outcome != profilelogin.OutcomeFresh {
			t.Fatalf("%s: %+v", id, got)
		}
	}
	if len(frank.renewed) != 0 {
		t.Fatalf("renewed = %v", frank.renewed)
	}
	off := false
	s.config.UsageMonitor.KeepProfileLoginsFresh = &off
	if newProfileLogins(s.config, nil, nil, nil, s.now) != nil {
		t.Fatal("keep_profile_logins_fresh: false must turn it off")
	}
}

// One sign-out, one notification, with the command to sign in; it repeats
// only after the login has worked again.
func TestASignedOutSeatNotifiesOncePerSignOut(t *testing.T) {
	s, frank, notifier, _, _ := keepAliveServer(t, time.Minute, true)
	frank.login = nativeusage.ProfileLogin{SignedOut: true}
	for range 3 {
		_ = s.monitorProvider(context.Background(), "frank")
	}
	if len(notifier.events) != 1 {
		t.Fatalf("events = %d", len(notifier.events))
	}
	e := notifier.events[0]
	if e.Type != domain.EventAccountNeedsLogin || e.ProviderAccountID != "frank" || !strings.Contains(e.Title, "Frank") ||
		!strings.Contains(e.Message, "CLAUDE_CONFIG_DIR=/p/.claude-cc-frank claude") {
		t.Fatalf("event = %+v", e)
	}
	if len(frank.renewed) != 0 {
		t.Fatal("a signed-out seat must not be run")
	}
	frank.login = nativeusage.ProfileLogin{ExpiresAt: s.now().Add(8 * time.Hour), HasRefreshToken: true}
	_ = s.monitorProvider(context.Background(), "frank")
	frank.login = nativeusage.ProfileLogin{SignedOut: true}
	_ = s.monitorProvider(context.Background(), "frank")
	if len(notifier.events) != 2 {
		t.Fatalf("after recovery and a second sign-out: events = %d", len(notifier.events))
	}
}

// A seat whose refresh token was revoked still has one in the keychain,
// so renewals just fail; after a few in a row it is reported once as
// needing a sign-in.
func TestASeatThatKeepsFailingToRenewNotifiesOnce(t *testing.T) {
	s, _, notifier, _, now := keepAliveServer(t, -time.Minute, false)
	for range 8 {
		_ = s.monitorProvider(context.Background(), "frank")
		*now = now.Add(profilelogin.DefaultBackoff)
	}
	if len(notifier.events) != 1 || notifier.events[0].Type != domain.EventAccountNeedsLogin {
		t.Fatalf("events = %+v", notifier.events)
	}
}
