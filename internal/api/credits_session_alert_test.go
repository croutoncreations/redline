package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/usage"
)

// sessionSource reads a credits account like the platform would: signed out
// while expired is true, a balance otherwise.
type sessionSource struct {
	expired *bool
	now     func() time.Time
}

type sessionExpired struct{}

func (sessionExpired) Error() string  { return "OpenAI rejected the saved session key" }
func (sessionExpired) Reason() string { return usage.ReasonSignedOut }

func (sessionSource) Name() string { return "native" }
func (s sessionSource) Fetch(_ context.Context, p config.Provider) (decision.UsageSnapshot, []byte, error) {
	if *s.expired {
		return decision.UsageSnapshot{}, nil, sessionExpired{}
	}
	return decision.UsageSnapshot{Provider: p.Provider, Source: "native", ObservedAt: s.now(),
		Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: s.now().Add(48 * time.Hour)}}, nil, nil
}

func creditsAlertServer(t *testing.T) (*Server, *bool, *redemptionNotifier) {
	t.Helper()
	s, _, _ := redemptionServer(t, "claude")
	now := s.now()
	s.now = func() time.Time { return now }
	s.config.Providers = map[string]config.Provider{
		"openai-credits": {Provider: config.ProviderAPICredits, Label: "Crouton", UsageSource: "native",
			WindowWeeklyCost: 0.05, APICredits: &config.APICredits{}},
		"main": {Provider: "claude", UsageSource: "native"},
	}
	expired := new(bool)
	source := sessionSource{expired: expired, now: s.now}
	s.usageSources = usage.NewManager(source, source, s.now)
	notifier := &redemptionNotifier{}
	s.notifier = notifier
	return s, expired, notifier
}

// An expired session key holds the account's work silently unless someone
// looks at a dashboard, so it is announced once, with the fix; it is
// announced again only after the key worked in between.
func TestExpiredCreditsSessionKeyNotifiesOncePerExpiry(t *testing.T) {
	s, expired, notifier := creditsAlertServer(t)
	*expired = true
	for range 3 {
		_ = s.monitorProvider(context.Background(), "openai-credits")
	}
	if len(notifier.events) != 1 {
		t.Fatalf("events = %d, want 1", len(notifier.events))
	}
	e := notifier.events[0]
	if e.Type != domain.EventAccountSessionKeyNeeded || e.ProviderAccountID != "openai-credits" ||
		!strings.Contains(e.Title, "Crouton") || !strings.Contains(e.Message, "redline account set-session-key openai-credits") {
		t.Fatalf("event = %+v", e)
	}
	if strings.Contains(e.Message, "sess-") {
		t.Fatal("the alert must not contain key material")
	}
	*expired = false
	_ = s.monitorProvider(context.Background(), "openai-credits")
	*expired = true
	_ = s.monitorProvider(context.Background(), "openai-credits")
	if len(notifier.events) != 2 {
		t.Fatalf("after recovery and a second expiry: events = %d", len(notifier.events))
	}
}

// Other accounts' sign-outs are not session-key alerts.
func TestSignedOutNonCreditsAccountSendsNoSessionKeyAlert(t *testing.T) {
	s, expired, notifier := creditsAlertServer(t)
	*expired = true
	_ = s.monitorProvider(context.Background(), "main")
	for _, e := range notifier.events {
		if e.Type == domain.EventAccountSessionKeyNeeded {
			t.Fatalf("unexpected %+v", e)
		}
	}
}

// A failed delivery is tried again on the next check.
func TestAFailedSessionKeyAlertIsRetried(t *testing.T) {
	s, expired, _ := creditsAlertServer(t)
	*expired = true
	failing := &failingNotifier{}
	s.notifier = failing
	_ = s.monitorProvider(context.Background(), "openai-credits")
	first := failing.calls
	_ = s.monitorProvider(context.Background(), "openai-credits")
	if first < 1 || failing.calls <= first {
		t.Fatalf("delivery attempts = %d then %d, want a retry", first, failing.calls)
	}
}

// Transient failures (network, rate limits) are not a session problem.
func TestTransientCreditsFailureSendsNoAlert(t *testing.T) {
	s, _, notifier := creditsAlertServer(t)
	s.noteCreditsSession(context.Background(), "openai-credits", s.config.Providers["openai-credits"], errors.New("network down"))
	if len(notifier.events) != 0 {
		t.Fatalf("events = %+v", notifier.events)
	}
}
