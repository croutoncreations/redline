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
	load  func() (string, error)
}

func (s controllerLicenseStore) Load(context.Context) (string, error) {
	if s.load != nil {
		return s.load()
	}
	return s.value, s.err
}
func (controllerLicenseStore) Replace(context.Context, string) error { return nil }
func (controllerLicenseStore) Clear(context.Context) error           { return nil }

type controllerCache struct {
	value    relay.CachedEntitlement
	exists   bool
	saved    chan relay.CachedEntitlement
	saveErrs []error
	mu       sync.Mutex
}

func (c *controllerCache) Load(string, time.Time) (relay.CachedEntitlement, bool, error) {
	return c.value, c.exists, nil
}
func (c *controllerCache) Save(value relay.CachedEntitlement) error {
	c.mu.Lock()
	c.value, c.exists = value, true
	var err error
	if len(c.saveErrs) > 0 {
		err = c.saveErrs[0]
		c.saveErrs = c.saveErrs[1:]
	}
	c.mu.Unlock()
	select {
	case c.saved <- value:
	default:
	}
	return err
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

func (c *controllerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *controllerClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}
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

func TestEntitlementControllerCoalescesRelaySignalsAndSupersedesOldCredentialWork(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(relay.EntitlementLifetime).Unix()
	calls := make(chan int, 4)
	var mu sync.Mutex
	count := 0
	issuer := controllerIssuerFunc(func(ctx context.Context, _, _, _ string, _ time.Time) (relay.Entitlement, error) {
		mu.Lock()
		count++
		call := count
		mu.Unlock()
		calls <- call
		switch call {
		case 1:
			return relay.Entitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
		case 2:
			<-ctx.Done()
			return relay.Entitlement{}, ctx.Err()
		default:
			return relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
		}
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 4)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}, Clock: clock,
		Issuer: issuer, Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	if call := <-calls; call != 1 {
		t.Fatalf("first call=%d", call)
	}
	<-clock.delays
	for i := 0; i < 100; i++ {
		controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	}
	if call := <-calls; call != 2 {
		t.Fatalf("relay-triggered call=%d", call)
	}
	for i := 0; i < 100; i++ {
		controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	}
	select {
	case call := <-calls:
		t.Fatalf("duplicate signal bypassed backoff/in-flight coalescing: call %d", call)
	case <-time.After(30 * time.Millisecond):
	}
	controller.TriggerLicenseChanged()
	if call := <-calls; call != 3 {
		t.Fatalf("replacement generation call=%d", call)
	}
	_ = waitRelayState(t, coordinator, RelayReadinessActive)
	cancel()
	<-done
}

func TestEntitlementControllerPublishesTerminalNoSeatAndRetriesOnTrigger(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	base.Seats, base.SeatsUsed, base.MaxClients = 9, 8, 7
	base.RenewsAt, base.ExpiresAt, base.UnavailableSince = now, now, now
	base.PersistenceDegraded = true
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
	if got.Seats != 0 || got.SeatsUsed != 0 || got.MaxClients != 0 || !got.RenewsAt.IsZero() || !got.ExpiresAt.IsZero() || !got.UnavailableSince.IsZero() || got.PersistenceDegraded {
		t.Fatalf("no-seat state retained stale fields: %#v", got)
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

func TestEntitlementControllerUsesResponseReceiptForCacheAndRenewal(t *testing.T) {
	requestAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	latency := 37 * time.Minute
	receivedAt := requestAt.Add(latency)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := receivedAt.Add(relay.EntitlementLifetime).Unix()
	clock := &controllerClock{now: requestAt, delays: make(chan time.Duration, 1)}
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: NewRelayCoordinator(base), Initial: base, Licenses: controllerLicenseStore{value: "license"}, Clock: clock, Cache: cache,
		Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
			clock.Advance(latency)
			return relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
		}),
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
		Jitter:  func() float64 { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	saved := <-cache.saved
	if saved.ObtainedAt != receivedAt.Unix() {
		t.Fatalf("obtained_at=%v want response receipt %v", time.Unix(saved.ObtainedAt, 0), receivedAt)
	}
	if delay := <-clock.delays; delay != relay.EntitlementLifetime/2 {
		t.Fatalf("renew delay=%v want=%v", delay, relay.EntitlementLifetime/2)
	}
	cancel()
	<-done
}

func TestEntitlementControllerDoesNotPublishRelayRejectedToken(t *testing.T) {
	for _, tc := range []struct {
		name       string
		refreshErr error
	}{
		{"issuer relay key mismatch", &relay.RelayRefreshError{Kind: relay.RelayRefreshAuth, Status: http.StatusUnauthorized}},
		{"relay transport failure", &relay.RelayRefreshError{Kind: relay.RelayRefreshUnavailable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
			base := hostedControllerBase()
			sid := relay.SessionSID(base.SessionID)
			oldExp := now.Add(2 * time.Hour).Unix()
			old := relay.CachedEntitlement{Token: controllerToken(t, oldExp, sid, 5), Exp: oldExp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5}
			newExp := now.Add(relay.EntitlementLifetime).Unix()
			cache := &controllerCache{value: old, exists: true, saved: make(chan relay.CachedEntitlement, 1)}
			coordinator := NewRelayCoordinator(base)
			clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
			controller := NewEntitlementController(EntitlementControllerOptions{
				Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache, Clock: clock,
				Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
					return relay.Entitlement{Token: controllerToken(t, newExp, sid, 5), Exp: newExp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
				}),
				Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return tc.refreshErr },
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { controller.Run(ctx); close(done) }()
			got := waitRelayState(t, coordinator, RelayReadinessRenewPending)
			if got.EntitlementToken.Value() != old.Token.Value() {
				t.Fatal("rejected token replaced the previous accepted token")
			}
			select {
			case <-cache.saved:
				t.Fatal("rejected token was persisted")
			default:
			}
			cancel()
			<-done
		})
	}
}

func TestEntitlementControllerKeepsEstablishedTokenOnTransientKeychainFailure(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(relay.EntitlementLifetime).Unix()
	var loads int
	store := controllerLicenseStore{load: func() (string, error) {
		loads++
		if loads == 1 {
			return "license", nil
		}
		return "", ErrLicenseStoreUnavailable
	}}
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: store, Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}, Clock: clock,
		Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
			return relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
		}),
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	_ = waitRelayState(t, coordinator, RelayReadinessActive)
	<-clock.delays
	controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	got := waitRelayState(t, coordinator, RelayReadinessRenewPending)
	if !got.CanDial() || got.EntitlementToken.Value() == "" {
		t.Fatalf("established token lost on transient keychain failure: %#v", got)
	}
	cancel()
	<-done
}

func TestEntitlementControllerRetriesDegradedPersistenceWithoutDiscardingToken(t *testing.T) {
	for _, failure := range []string{"disk full", "symlink target refused", "directory fsync failed"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
			base := hostedControllerBase()
			sid := relay.SessionSID(base.SessionID)
			exp := now.Add(relay.EntitlementLifetime).Unix()
			cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 3), saveErrs: []error{errors.New(failure), nil}}
			coordinator := NewRelayCoordinator(base)
			clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
			controller := NewEntitlementController(EntitlementControllerOptions{
				Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache, Clock: clock,
				Issuer: controllerIssuerFunc(func(context.Context, string, string, string, time.Time) (relay.Entitlement, error) {
					return relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
				}),
				Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { controller.Run(ctx); close(done) }()
			_ = waitRelayState(t, coordinator, RelayReadinessActive)
			deadline := time.Now().Add(time.Second)
			got := coordinator.Current()
			for !got.PersistenceDegraded && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
				got = coordinator.Current()
			}
			if !got.CanDial() || !got.PersistenceDegraded {
				t.Fatalf("persistence failure discarded token or warning: %#v", got)
			}
			<-clock.delays
			controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) && coordinator.Current().PersistenceDegraded {
				time.Sleep(time.Millisecond)
			}
			if coordinator.Current().PersistenceDegraded {
				t.Fatal("successful persistence retry did not clear warning")
			}
			cancel()
			<-done
		})
	}
}
