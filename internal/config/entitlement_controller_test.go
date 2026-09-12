package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jfox/redline/internal/relay"
)

type controllerLicenseStore struct {
	value string
	err   error
}

func (s controllerLicenseStore) Load(context.Context) (string, error) { return s.value, s.err }
func (controllerLicenseStore) Replace(context.Context, string) error  { return nil }
func (controllerLicenseStore) Clear(context.Context) error            { return nil }

type controllerCache struct {
	value  relay.CachedEntitlement
	exists bool
	saved  chan relay.CachedEntitlement
}

func (c *controllerCache) Load(string, time.Time) (relay.CachedEntitlement, bool, error) {
	return c.value, c.exists, nil
}
func (c *controllerCache) Save(value relay.CachedEntitlement) error {
	c.value, c.exists = value, true
	select {
	case c.saved <- value:
	default:
	}
	return nil
}

type controllerIssuerFunc func(context.Context, string, string, string, time.Time) (relay.Entitlement, error)

func (f controllerIssuerFunc) Entitlement(ctx context.Context, key, sid, label string, now time.Time) (relay.Entitlement, error) {
	return f(ctx, key, sid, label, now)
}

type controllerTimer struct{ ch chan time.Time }

func (t *controllerTimer) C() <-chan time.Time { return t.ch }
func (t *controllerTimer) Stop() bool          { return true }

type controllerClock struct {
	now    time.Time
	mu     sync.Mutex
	delays chan time.Duration
}

func (c *controllerClock) Now() time.Time { return c.now }
func (c *controllerClock) NewTimer(delay time.Duration) entitlementTimer {
	c.delays <- delay
	return &controllerTimer{ch: make(chan time.Time)}
}

func controllerToken(t *testing.T, exp int64, sid string, max int) relay.Secret {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"exp": exp, "sid": sid, "max_clients": max})
	if err != nil {
		t.Fatal(err)
	}
	return relay.NewSecret(base64.StdEncoding.EncodeToString(raw) + "." + base64.StdEncoding.EncodeToString(make([]byte, 64)))
}

func waitRelayState(t *testing.T, coordinator *RelayCoordinator, state RelayReadiness) ResolvedRelay {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := coordinator.Current(); got.Readiness == state {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state=%s, want %s", coordinator.Current().Readiness, state)
	return ResolvedRelay{}
}

func hostedControllerBase() ResolvedRelay {
	return ResolvedRelay{RelayManagedState: RelayManagedState{
		Mode: RelayModeHosted, URL: "https://relay.example.com", IssuerURL: "https://issuer.example.com", SessionID: "session-controller-123456",
	}, Readiness: RelayReadinessHostedConfigured}
}

func TestEntitlementControllerKeepsValidCacheUsableDuringStartupRenewal(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(12 * 24 * time.Hour).Unix()
	cached := relay.CachedEntitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Add(-2 * 24 * time.Hour).Unix(), SID: sid, MaxClients: 5}
	started := make(chan struct{})
	issuer := controllerIssuerFunc(func(ctx context.Context, _, _, _ string, _ time.Time) (relay.Entitlement, error) {
		close(started)
		<-ctx.Done()
		return relay.Entitlement{}, ctx.Err()
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"},
		Issuer: issuer, Cache: &controllerCache{value: cached, exists: true, saved: make(chan relay.CachedEntitlement, 1)}, Clock: clock,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	<-started
	got := waitRelayState(t, coordinator, RelayReadinessActive)
	if !got.CanDial() || got.EntitlementToken.Value() != cached.Token.Value() {
		t.Fatalf("cached startup snapshot=%#v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller leaked after cancellation")
	}
}

func TestEntitlementControllerRenewsWithInjectedJitterWithoutSocketRestart(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(relay.EntitlementLifetime).Unix()
	issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 2, SeatsUsed: 1}
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 1)}
	refreshes := make(chan relay.Secret, 1)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache, Clock: clock, Jitter: func() float64 { return 1 },
		Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
			return issued, nil
		}),
		Refresh: func(_ context.Context, _ *http.Client, _, _ string, token relay.Secret) error {
			refreshes <- token
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	got := waitRelayState(t, coordinator, RelayReadinessActive)
	if !got.CanDial() || got.ReconnectGeneration != 0 || got.Seats != 2 || got.MaxClients != 5 {
		t.Fatalf("active snapshot=%#v", got)
	}
	if token := <-refreshes; token.Value() != issued.Token.Value() {
		t.Fatal("refresh did not receive issued token")
	}
	wantDelay := time.Duration(float64(relay.EntitlementLifetime/2) * .9)
	if delay := <-clock.delays; delay != wantDelay {
		t.Fatalf("renew delay=%v want=%v", delay, wantDelay)
	}
	cancel()
	<-done
}

func TestEntitlementControllerPublishesTerminalNoSeatAndRetriesOnTrigger(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	calls := make(chan struct{}, 2)
	issuer := controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
		calls <- struct{}{}
		return relay.Entitlement{}, &relay.IssuerError{Kind: relay.IssuerNoSeat, Activations: []relay.ActivationSummary{{Label: "First Mac", FirstSeen: now}}}
	})
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Issuer: issuer,
		Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 1)}, Clock: clock,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	<-calls
	got := waitRelayState(t, coordinator, RelayReadinessNoSeat)
	if got.CanDial() || len(got.Activations()) != 1 || got.Activations()[0].Label != "First Mac" {
		t.Fatalf("no-seat snapshot=%#v", got)
	}
	if delay := <-clock.delays; delay != terminalEntitlementRetry {
		t.Fatalf("terminal retry=%v", delay)
	}
	controller.Trigger()
	<-calls
	cancel()
	<-done
}

func TestEntitlementControllerUsesValidCacheForTransientRenewPending(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(2 * time.Hour).Unix()
	cached := relay.CachedEntitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5}
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Clock: clock,
		Cache: &controllerCache{value: cached, exists: true, saved: make(chan relay.CachedEntitlement, 1)},
		Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
			return relay.Entitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	got := waitRelayState(t, coordinator, RelayReadinessRenewPending)
	if !got.CanDial() || !got.ExpiresAt.Equal(time.Unix(exp, 0)) || got.EntitlementToken.Value() != cached.Token.Value() {
		t.Fatalf("renew-pending snapshot=%#v", got)
	}
	if delay := <-clock.delays; delay != initialEntitlementRetry {
		t.Fatalf("retry delay=%v", delay)
	}
	cancel()
	<-done
}

func TestEntitlementControllerPublishesUnavailableWhenKeychainCannotBeRead(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{err: ErrLicenseStoreUnavailable}, Clock: clock,
		Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 1)},
		Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
			return relay.Entitlement{}, errors.New("must not call issuer")
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || !got.UnavailableSince.Equal(now) {
		t.Fatalf("unavailable snapshot=%#v", got)
	}
	<-clock.delays
	cancel()
	<-done
}
