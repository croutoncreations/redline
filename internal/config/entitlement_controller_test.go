package config

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
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
	saveErr  error
	mu       sync.Mutex
}

func (c *controllerCache) Load(_ string, fingerprint string, _ time.Time) (relay.CachedEntitlement, bool, error) {
	value := c.value
	if value.SchemaVersion == 0 {
		value.SchemaVersion = relay.EntitlementCacheSchemaVersion
		value.CredentialFingerprint = fingerprint
	}
	return value, c.exists, nil
}
func (c *controllerCache) SaveContext(_ context.Context, value relay.CachedEntitlement) error {
	c.mu.Lock()
	c.value, c.exists = value, true
	var err error
	if len(c.saveErrs) > 0 {
		err = c.saveErrs[0]
		c.saveErrs = c.saveErrs[1:]
	} else {
		err = c.saveErr
	}
	c.mu.Unlock()
	select {
	case c.saved <- value:
	default:
	}
	return err
}

type blockingControllerCache struct {
	started   chan relay.CachedEntitlement
	release   chan struct{}
	persisted chan relay.CachedEntitlement
	mu        sync.Mutex
	current   relay.CachedEntitlement
	calls     int
}

func (c *blockingControllerCache) Load(string, string, time.Time) (relay.CachedEntitlement, bool, error) {
	return relay.CachedEntitlement{}, false, nil
}

func (c *blockingControllerCache) SaveContext(ctx context.Context, value relay.CachedEntitlement) error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	c.started <- value
	if call == 1 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.release:
		}
	}
	c.mu.Lock()
	c.current = value
	c.mu.Unlock()
	c.persisted <- value
	return nil
}

type controllerIssuerFunc func(context.Context, string, string, string, time.Time) (relay.Entitlement, error)

type controllerReceivedIssuerFunc func(context.Context, string, string, string) (relay.ReceivedEntitlement, error)

func (f controllerReceivedIssuerFunc) Entitlement(ctx context.Context, key, sid, label string) (relay.ReceivedEntitlement, error) {
	return f(ctx, key, sid, label)
}

func (f controllerIssuerFunc) Entitlement(ctx context.Context, key, sid, label string) (relay.ReceivedEntitlement, error) {
	issued, err := f(ctx, key, sid, label, time.Time{})
	receivedAt := time.Time{}
	if issued.Exp != 0 {
		receivedAt = time.Unix(issued.Exp, 0).Add(-relay.EntitlementLifetime)
	}
	return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: receivedAt}, err
}

type controllerTimer struct{ ch chan time.Time }

func (t *controllerTimer) C() <-chan time.Time { return t.ch }
func (t *controllerTimer) Stop() bool          { return true }

type controllerClock struct {
	now    time.Time
	mu     sync.Mutex
	delays chan time.Duration
}

type controllableExpiryClock struct {
	timers chan *controllerTimer
}

func (c *controllableExpiryClock) Now() time.Time { return time.Time{} }
func (c *controllableExpiryClock) NewTimer(time.Duration) entitlementTimer {
	timer := &controllerTimer{ch: make(chan time.Time, 1)}
	c.timers <- timer
	return timer
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
	if got.Seats != 0 || got.SeatsUsed != 0 || got.MaxClients != 0 || !got.RenewsAt.IsZero() || !got.ExpiresAt.IsZero() || !got.UnavailableSince.IsZero() {
		t.Fatalf("no-seat state retained stale authority fields: %#v", got)
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

func TestEntitlementControllerExactExpirationPublishesUnavailableWithBackoff(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(time.Hour).Unix()
	cached := relay.CachedEntitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5}
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: NewRelayCoordinator(base), Initial: base, Licenses: controllerLicenseStore{value: "license"}, Clock: clock,
		Cache: &controllerCache{value: cached, exists: true, saved: make(chan relay.CachedEntitlement, 1)},
		Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			return relay.ReceivedEntitlement{ReceivedAt: clock.Now()}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
		}),
	})
	coordinator := controller.opts.Coordinator
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	_ = waitRelayState(t, coordinator, RelayReadinessRenewPending)
	if delay := <-clock.delays; delay <= 0 {
		t.Fatalf("pre-expiry retry delay=%v", delay)
	}
	clock.Advance(time.Hour)
	controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || !got.UnavailableSince.Equal(time.Unix(exp, 0)) {
		t.Fatalf("exact-expiry snapshot=%#v", got)
	}
	if delay := <-clock.delays; delay <= 0 {
		t.Fatalf("exact-expiry retry hot-looped with delay=%v", delay)
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

func TestEntitlementControllerExcludesKeychainLatencyFromIssuerReceipt(t *testing.T) {
	startedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	clock := &controllerClock{now: startedAt, delays: make(chan time.Duration, 1)}
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	keychainDelay := 2 * relay.EntitlementClockSkew
	store := controllerLicenseStore{load: func() (string, error) {
		clock.Advance(keychainDelay)
		return "license", nil
	}}
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		receivedAt := clock.Now()
		exp := receivedAt.Add(relay.EntitlementLifetime).Unix()
		issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: receivedAt}, nil
	})
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 1)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: NewRelayCoordinator(base), Initial: base, Licenses: store, Issuer: issuer, Cache: cache, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	saved := <-cache.saved
	wantReceipt := startedAt.Add(keychainDelay).Unix()
	if saved.ObtainedAt != wantReceipt || saved.Exp != time.Unix(wantReceipt, 0).Add(relay.EntitlementLifetime).Unix() {
		t.Fatalf("cache used caller time instead of issuer receipt: %#v", saved)
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

func TestEntitlementControllerPersistenceFailureCannotBlockAuthority(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 10)}
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 20), saveErr: errors.New("permanent disk failure")}
	calls := make(chan int, 10)
	releaseThird := make(chan struct{})
	fourthCanceled := make(chan struct{})
	var mu sync.Mutex
	callCount := 0
	issuer := controllerReceivedIssuerFunc(func(ctx context.Context, _, _, _ string) (relay.ReceivedEntitlement, error) {
		mu.Lock()
		callCount++
		call := callCount
		mu.Unlock()
		calls <- call
		if call == 3 {
			select {
			case <-ctx.Done():
				return relay.ReceivedEntitlement{}, ctx.Err()
			case <-releaseThird:
			}
		}
		if call == 4 {
			<-ctx.Done()
			close(fourthCanceled)
			return relay.ReceivedEntitlement{}, ctx.Err()
		}
		receivedAt := clock.Now().UTC().Truncate(time.Second)
		exp := receivedAt.Add(relay.EntitlementLifetime).Unix()
		signature := make([]byte, 64)
		signature[0] = byte(call)
		claims, _ := json.Marshal(map[string]any{"exp": exp, "sid": sid, "max_clients": 5})
		token := relay.NewSecret(base64.StdEncoding.EncodeToString(claims) + "." + base64.StdEncoding.EncodeToString(signature))
		issued := relay.Entitlement{Token: token, Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: receivedAt}, nil
	})
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache, Clock: clock,
		Issuer: issuer, Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	if call := <-calls; call != 1 {
		t.Fatalf("first issuer call=%d", call)
	}
	first := waitRelayState(t, coordinator, RelayReadinessActive)
	deadline := time.Now().Add(time.Second)
	for !coordinator.Current().PersistenceDegraded && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !coordinator.Current().PersistenceDegraded {
		t.Fatal("permanent cache failure was not observable")
	}
	<-clock.delays
	clock.Advance(time.Second)
	controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	if call := <-calls; call != 2 {
		t.Fatalf("persistence retry short-circuited issuer renewal: call=%d", call)
	}
	deadline = time.Now().Add(time.Second)
	for coordinator.Current().EntitlementToken.Value() == first.EntitlementToken.Value() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	second := coordinator.Current()
	if second.EntitlementToken.Value() == first.EntitlementToken.Value() || !second.PersistenceDegraded {
		t.Fatalf("new accepted authority did not supersede pending persistence: %#v", second)
	}
	<-clock.delays

	secondExp := clock.Now().Add(relay.EntitlementLifetime).Unix()
	clock.Advance(time.Unix(secondExp, 0).Sub(clock.Now()))
	controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	if call := <-calls; call != 3 {
		t.Fatalf("expiry renewal call=%d", call)
	}
	expired := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if expired.CanDial() || !expired.PersistenceDegraded {
		t.Fatalf("raw expiration retained authority or hid cache degradation: %#v", expired)
	}
	close(releaseThird)
	third := waitRelayState(t, coordinator, RelayReadinessActive)
	<-clock.delays
	controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	if call := <-calls; call != 4 {
		t.Fatalf("pre-replacement issuer call=%d", call)
	}
	clock.Advance(time.Second)
	controller.TriggerLicenseChanged()
	select {
	case <-fourthCanceled:
	case <-time.After(time.Second):
		t.Fatal("license replacement did not cancel obsolete issuer request")
	}
	if call := <-calls; call != 5 {
		t.Fatalf("replacement issuer call=%d", call)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := coordinator.Current()
		if got.EntitlementToken.Value() != third.EntitlementToken.Value() && got.PersistenceDegraded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := coordinator.Current(); got.EntitlementToken.Value() == third.EntitlementToken.Value() || !got.PersistenceDegraded {
		t.Fatalf("replacement authority was blocked by obsolete persistence: %#v", got)
	}
	cancel()
	<-done
}

func TestEntitlementControllerRetainsRelaySignalQueuedDuringAcceptance(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	calls := make(chan int, 2)
	count := 0
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		count++
		calls <- count
		exp := now.Add(relay.EntitlementLifetime).Unix()
		issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
	})
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"},
		Issuer: issuer, Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	// Queue a relay authority event while no request exists to cancel. Acceptance
	// must not blanket-drain it as stale.
	controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	if call := <-calls; call != 1 {
		t.Fatalf("first issuer call=%d", call)
	}
	if call := <-calls; call != 2 {
		t.Fatalf("queued relay signal was lost during acceptance: call=%d", call)
	}
	cancel()
	<-done
}

func TestEntitlementControllerTerminalDenialRevokesFallbackForCredentialGeneration(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(time.Hour).Unix()
	cached := relay.CachedEntitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5}
	calls := 0
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		calls++
		if calls == 1 {
			return relay.ReceivedEntitlement{ReceivedAt: now}, &relay.IssuerError{Kind: relay.IssuerLapsed}
		}
		return relay.ReceivedEntitlement{ReceivedAt: now}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Issuer: issuer,
		Cache: &controllerCache{value: cached, exists: true, saved: make(chan relay.CachedEntitlement, 1)}, Clock: clock,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	_ = waitRelayState(t, coordinator, RelayReadinessLapsed)
	<-clock.delays
	controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || got.EntitlementToken.Value() != "" {
		t.Fatalf("terminally revoked fallback was republished: %#v", got)
	}
	cancel()
	<-done
}

func TestEntitlementControllerNewCredentialGenerationCanEstablishAuthorityAfterTerminalDenial(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	newExp := now.Add(relay.EntitlementLifetime).Unix()
	newToken := controllerToken(t, newExp, sid, 5)
	calls := 0
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		calls++
		if calls == 1 {
			return relay.ReceivedEntitlement{ReceivedAt: now}, &relay.IssuerError{Kind: relay.IssuerInvalidKey}
		}
		return relay.ReceivedEntitlement{Entitlement: relay.Entitlement{Token: newToken, Exp: newExp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, ReceivedAt: now}, nil
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "new-license"}, Issuer: issuer,
		Cache: cache, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	_ = waitRelayState(t, coordinator, RelayReadinessInvalidKey)
	<-clock.delays
	controller.TriggerLicenseChanged()
	got := waitRelayState(t, coordinator, RelayReadinessActive)
	if !got.CanDial() || got.EntitlementToken.Value() != newToken.Value() {
		t.Fatalf("new credential generation did not establish authority: %#v", got)
	}
	var cached relay.CachedEntitlement
	for i := 0; i < 2; i++ {
		candidate := <-cache.saved
		if !candidate.Revoked {
			cached = candidate
			break
		}
	}
	if cached.Token.Value() != newToken.Value() || cached.CredentialFingerprint != relay.CredentialFingerprint("new-license") {
		t.Fatalf("new credential authority was not durably credential-bound: %#v", cached)
	}
	cancel()
	<-done
}

func TestEntitlementControllerResamplesClockAfterKeychainWorkBeforeFallback(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(time.Hour).Unix()
	cached := relay.CachedEntitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5}
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	store := controllerLicenseStore{load: func() (string, error) {
		clock.Advance(time.Hour)
		return "", ErrLicenseStoreUnavailable
	}}
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: store,
		Cache: &controllerCache{value: cached, exists: true, saved: make(chan relay.CachedEntitlement, 1)}, Clock: clock,
		Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			t.Fatal("issuer called after keychain failure")
			return relay.ReceivedEntitlement{}, nil
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || !got.UnavailableSince.Equal(time.Unix(exp, 0)) {
		t.Fatalf("expiration crossing during keychain work published stale fallback: %#v", got)
	}
	cancel()
	<-done
}

func TestEntitlementControllerExactExpirationBetweenValidationAndCommitIsUnavailable(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(time.Minute).Unix()
	cache := &controllerCache{saved: make(chan relay.CachedEntitlement, 1)}
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 1)}
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache, Clock: clock,
		Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
			return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
		}),
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error {
			return &relay.RelayRefreshError{Kind: relay.RelayRefreshNoHost, Status: http.StatusLocked}
		},
		BeforeCommit: func() {
			clock.Advance(time.Minute)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || got.EntitlementToken.Value() != "" || !got.UnavailableSince.Equal(time.Unix(exp, 0)) {
		t.Fatalf("exact expiration crossing published authority: %#v", got)
	}
	select {
	case <-cache.saved:
		t.Fatal("expired issuer result was enqueued for persistence")
	default:
	}
	cancel()
	<-done
}

func TestEntitlementControllerCredentialGenerationCommitIsAtomic(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	oldExp := now.Add(relay.EntitlementLifetime - time.Hour).Unix()
	oldToken := controllerToken(t, oldExp, sid, 4)
	newExp := now.Add(relay.EntitlementLifetime).Unix()
	newToken := controllerToken(t, newExp, sid, 5)
	beforeCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	calls := 0
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		calls++
		issued := relay.Entitlement{Token: oldToken, Exp: oldExp, MaxClients: 4, Seats: 1, SeatsUsed: 1}
		if calls > 1 {
			issued.Token, issued.Exp, issued.MaxClients = newToken, newExp, 5
		}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 2)}
	hookCalls := 0
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Issuer: issuer,
		Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
		BeforeCommit: func() {
			hookCalls++
			if hookCalls == 1 {
				close(beforeCommit)
				<-releaseCommit
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	<-beforeCommit
	controller.TriggerLicenseChanged()
	close(releaseCommit)
	got := waitRelayState(t, coordinator, RelayReadinessActive)
	if got.EntitlementToken.Value() != newToken.Value() {
		t.Fatalf("obsolete issuer attempt won generation race: %#v", got)
	}
	cancel()
	<-done
}

func TestEntitlementControllerBlockedPersistenceDoesNotBlockExpiryRenewalOrGeneration(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 5)}
	cache := &blockingControllerCache{
		started: make(chan relay.CachedEntitlement, 3), release: make(chan struct{}), persisted: make(chan relay.CachedEntitlement, 3),
	}
	issuerStarted := make(chan int, 3)
	releaseSecond := make(chan struct{})
	calls := 0
	issuer := controllerReceivedIssuerFunc(func(ctx context.Context, _, _, _ string) (relay.ReceivedEntitlement, error) {
		calls++
		call := calls
		issuerStarted <- call
		if call == 2 {
			select {
			case <-ctx.Done():
				return relay.ReceivedEntitlement{}, ctx.Err()
			case <-releaseSecond:
			}
		}
		receivedAt := clock.Now()
		exp := receivedAt.Add(relay.EntitlementLifetime).Unix()
		if call == 1 {
			exp = receivedAt.Add(time.Hour).Unix()
		}
		// Make each otherwise-identical token distinguishable without changing
		// its validated claims.
		claims := strings.Split(controllerToken(t, exp, sid, 5).Value(), ".")[0]
		signature := make([]byte, 64)
		signature[0] = byte(call)
		token := relay.NewSecret(claims + "." + base64.StdEncoding.EncodeToString(signature))
		issued := relay.Entitlement{Token: token, Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: receivedAt}, nil
	})
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Issuer: issuer, Cache: cache, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	if call := <-issuerStarted; call != 1 {
		t.Fatalf("initial issuer call=%d", call)
	}
	first := waitRelayState(t, coordinator, RelayReadinessActive)
	blocked := <-cache.started
	if blocked.Token.Value() != first.EntitlementToken.Value() || !first.PersistenceDegraded {
		t.Fatalf("blocked persistence was not observable: %#v", first)
	}
	clock.Advance(time.Hour)
	controller.TriggerRelayEntitlement(relay.EntitlementExpired)
	if call := <-issuerStarted; call != 2 {
		t.Fatalf("expiry issuer call=%d", call)
	}
	expired := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if expired.CanDial() || !expired.PersistenceDegraded {
		t.Fatalf("blocked Save retained expired dial authority: %#v", expired)
	}
	close(releaseSecond)
	second := waitRelayState(t, coordinator, RelayReadinessActive)
	if second.EntitlementToken.Value() == first.EntitlementToken.Value() {
		t.Fatal("renewal did not advance in-memory authority while Save was blocked")
	}
	controller.TriggerLicenseChanged()
	if call := <-issuerStarted; call != 3 {
		t.Fatalf("replacement generation issuer call=%d", call)
	}
	third := waitRelayState(t, coordinator, RelayReadinessActive)
	if third.EntitlementToken.Value() == second.EntitlementToken.Value() {
		t.Fatal("new credential generation did not supersede blocked persistence")
	}
	close(cache.release)
	if firstPersisted := <-cache.persisted; firstPersisted.Token.Value() != first.EntitlementToken.Value() {
		t.Fatal("unexpected first in-flight persistence value")
	}
	latestPersisted := <-cache.persisted
	if latestPersisted.Token.Value() != third.EntitlementToken.Value() {
		t.Fatalf("obsolete completion suppressed latest persistence: got %q want newest", latestPersisted.Token)
	}
	select {
	case extra := <-cache.persisted:
		t.Fatalf("obsolete intermediate token was persisted: %q", extra.Token)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller did not shut down after persistence worker completed")
	}
}

func TestEntitlementControllerObsoleteSaveCannotUndoTerminalRevocation(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(relay.EntitlementLifetime).Unix()
	cache := &blockingControllerCache{
		started: make(chan relay.CachedEntitlement, 2), release: make(chan struct{}), persisted: make(chan relay.CachedEntitlement, 2),
	}
	calls := make(chan int, 2)
	count := 0
	issuer := controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		count++
		calls <- count
		if count == 2 {
			return relay.ReceivedEntitlement{ReceivedAt: now}, &relay.IssuerError{Kind: relay.IssuerInvalidKey}
		}
		issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
	})
	coordinator := NewRelayCoordinator(base)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 3)}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"}, Issuer: issuer, Cache: cache, Clock: clock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	<-calls
	_ = waitRelayState(t, coordinator, RelayReadinessActive)
	oldSave := <-cache.started
	controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	<-calls
	terminal := waitRelayState(t, coordinator, RelayReadinessInvalidKey)
	if terminal.CanDial() {
		t.Fatalf("terminal runtime revocation waited for persistence: %#v", terminal)
	}
	close(cache.release)
	if persisted := <-cache.persisted; persisted.Token.Value() != oldSave.Token.Value() {
		t.Fatal("test did not release the obsolete in-flight save")
	}
	if tombstone := <-cache.persisted; !tombstone.Revoked || tombstone.CredentialFingerprint != relay.CredentialFingerprint("license") {
		t.Fatalf("obsolete save was not followed by bound tombstone: %#v", tombstone)
	}
	cache.mu.Lock()
	current := cache.current
	cache.mu.Unlock()
	if !current.Revoked || current.Token.Value() != "" {
		t.Fatalf("restart-visible record revived obsolete authority: %#v", current)
	}
	cancel()
	<-done
}

func TestEntitlementControllerExpiryWatcherRevokesWhileIssuerIsBlocked(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(time.Hour).Unix()
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 3)}
	expiryClock := &controllableExpiryClock{timers: make(chan *controllerTimer, 2)}
	issuerBlocked := make(chan struct{})
	calls := 0
	issuer := controllerReceivedIssuerFunc(func(ctx context.Context, _, _, _ string) (relay.ReceivedEntitlement, error) {
		calls++
		if calls > 1 {
			close(issuerBlocked)
			<-ctx.Done()
			return relay.ReceivedEntitlement{}, ctx.Err()
		}
		issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
		return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
	})
	coordinator := NewRelayCoordinator(base)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "license"},
		Issuer: issuer, Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 2)}, Clock: clock, ExpiryClock: expiryClock,
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	_ = waitRelayState(t, coordinator, RelayReadinessActive)
	expiryTimer := <-expiryClock.timers
	<-clock.delays
	controller.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
	<-issuerBlocked
	clock.Advance(time.Hour)
	expiryTimer.ch <- clock.Now()
	got := waitRelayState(t, coordinator, RelayReadinessUnavailable)
	if got.CanDial() || got.EntitlementToken.Value() != "" {
		t.Fatalf("raw expiration watcher retained blocked authority: %#v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked renewal or expiry watcher leaked on shutdown")
	}
}

func TestEntitlementControllerRelayEventAfterRefreshAcceptanceForcesFollowup(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	clock := &controllerClock{now: now, delays: make(chan time.Duration, 3)}
	calls := make(chan int, 3)
	count := 0
	controllerRef := (*EntitlementController)(nil)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: NewRelayCoordinator(base), Initial: base, Licenses: controllerLicenseStore{value: "license"},
		Cache: &controllerCache{saved: make(chan relay.CachedEntitlement, 3)}, Clock: clock,
		Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			count++
			calls <- count
			exp := now.Add(relay.EntitlementLifetime).Unix()
			issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
			return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
		}),
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
		BeforeCommit: func() {
			if count == 1 {
				controllerRef.TriggerRelayEntitlement(relay.EntitlementExpired)
			}
		},
	})
	controllerRef = controller
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	if call := <-calls; call != 1 {
		t.Fatalf("initial call=%d", call)
	}
	if call := <-calls; call != 2 {
		t.Fatalf("post-acceptance relay event was lost: call=%d", call)
	}
	cancel()
	<-done
}

func TestEntitlementControllerTerminalTombstoneSurvivesRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	fingerprint := relay.CredentialFingerprint("durable-license")
	exp := now.Add(time.Hour).Unix()
	cache := relay.NewEntitlementCacheStore(filepath.Join(t.TempDir(), "relay-entitlement.json"))
	if err := cache.Save(relay.CachedEntitlement{
		SchemaVersion: relay.EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: controllerToken(t, exp, sid, 5), Exp: exp, ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	}); err != nil {
		t.Fatal(err)
	}
	run := func(issuerErr *relay.IssuerError) (*RelayCoordinator, context.CancelFunc, <-chan struct{}) {
		coordinator := NewRelayCoordinator(base)
		controller := NewEntitlementController(EntitlementControllerOptions{
			Coordinator: coordinator, Initial: base, Licenses: controllerLicenseStore{value: "durable-license"}, Cache: cache,
			Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
				return relay.ReceivedEntitlement{ReceivedAt: time.Now().UTC().Truncate(time.Second)}, issuerErr
			}),
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { controller.Run(ctx); close(done) }()
		return coordinator, cancel, done
	}
	first, cancelFirst, doneFirst := run(&relay.IssuerError{Kind: relay.IssuerLapsed})
	_ = waitRelayState(t, first, RelayReadinessLapsed)
	deadline := time.Now().Add(time.Second)
	for first.Current().PersistenceDegraded && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancelFirst()
	<-doneFirst
	if _, valid, err := cache.Load(sid, fingerprint, now); err == nil || valid {
		t.Fatal("terminal denial did not durably replace cached authority")
	}
	second, cancelSecond, doneSecond := run(&relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true})
	got := waitRelayState(t, second, RelayReadinessUnavailable)
	if got.CanDial() || got.EntitlementToken.Value() != "" {
		t.Fatalf("restart revived terminally denied cache: %#v", got)
	}
	cancelSecond()
	<-doneSecond
}

func TestEntitlementControllerCancellationStopsBlockedPersistenceWorker(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	base := hostedControllerBase()
	sid := relay.SessionSID(base.SessionID)
	exp := now.Add(relay.EntitlementLifetime).Unix()
	cache := &blockingControllerCache{
		started: make(chan relay.CachedEntitlement, 1), release: make(chan struct{}), persisted: make(chan relay.CachedEntitlement, 1),
	}
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: NewRelayCoordinator(base), Initial: base, Licenses: controllerLicenseStore{value: "license"}, Cache: cache,
		Clock: &controllerClock{now: now, delays: make(chan time.Duration, 1)},
		Issuer: controllerReceivedIssuerFunc(func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			issued := relay.Entitlement{Token: controllerToken(t, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
			return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: now}, nil
		}),
		Refresh: func(context.Context, *http.Client, string, string, relay.Secret) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { controller.Run(ctx); close(done) }()
	<-cache.started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("context-aware blocked Save leaked the controller worker")
	}
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
