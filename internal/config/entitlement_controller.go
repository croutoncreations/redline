package config

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/jfox/redline/internal/relay"
)

const (
	initialEntitlementRetry  = time.Minute
	maximumEntitlementRetry  = 6 * time.Hour
	terminalEntitlementRetry = 6 * time.Hour
)

type entitlementIssuer interface {
	Entitlement(context.Context, string, string, string, time.Time) (relay.Entitlement, error)
}

type entitlementCache interface {
	Load(string, time.Time) (relay.CachedEntitlement, bool, error)
	Save(relay.CachedEntitlement) error
}

type entitlementTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type entitlementClock interface {
	Now() time.Time
	NewTimer(time.Duration) entitlementTimer
}

type realEntitlementClock struct{}

func (realEntitlementClock) Now() time.Time { return time.Now() }
func (realEntitlementClock) NewTimer(delay time.Duration) entitlementTimer {
	return realEntitlementTimer{Timer: time.NewTimer(delay)}
}

type realEntitlementTimer struct{ *time.Timer }

func (t realEntitlementTimer) C() <-chan time.Time { return t.Timer.C }

// EntitlementControllerOptions exposes deterministic seams used by tests. A
// nil seam selects the production implementation.
type EntitlementControllerOptions struct {
	Coordinator *RelayCoordinator
	Initial     ResolvedRelay
	Licenses    LicenseStore
	Issuer      entitlementIssuer
	Cache       entitlementCache
	RelayHTTP   *http.Client
	Clock       entitlementClock
	Jitter      func() float64
	Refresh     func(context.Context, *http.Client, string, string, relay.Secret) error
}

// EntitlementController is the sole writer of hosted runtime entitlement
// state. Trigger is non-blocking and coalesces concurrent relay/key events.
type EntitlementController struct {
	opts     EntitlementControllerOptions
	triggers chan struct{}
	mu       sync.Mutex
	running  bool
}

func NewEntitlementController(opts EntitlementControllerOptions) *EntitlementController {
	if opts.Clock == nil {
		opts.Clock = realEntitlementClock{}
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64
	}
	if opts.Refresh == nil {
		opts.Refresh = relay.RefreshRelayEntitlement
	}
	return &EntitlementController{opts: opts, triggers: make(chan struct{}, 1)}
}

func (c *EntitlementController) Trigger() {
	select {
	case c.triggers <- struct{}{}:
	default:
	}
}

func (c *EntitlementController) Run(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	base := c.opts.Initial
	switch base.Mode {
	case RelayModeOff:
		base.Readiness, base.Dial = RelayReadinessOff, false
		c.opts.Coordinator.Update(base)
		<-ctx.Done()
		return
	case RelayModeSelfHosted:
		base.Readiness, base.Dial = RelayReadinessSelfHosted, true
		c.opts.Coordinator.Update(base)
		<-ctx.Done()
		return
	}

	now := c.opts.Clock.Now()
	sid := relay.SessionSID(base.SessionID)
	var current relay.CachedEntitlement
	var valid bool
	// Resolver has already established that Keychain was readable and held a
	// key before a cache may make the hosted relay usable. In particular, never
	// publish a brief active state when startup resolution said unavailable.
	if base.Readiness == RelayReadinessHostedConfigured || base.Readiness == RelayReadinessActive || base.Readiness == RelayReadinessRenewPending {
		current, valid, _ = c.opts.Cache.Load(sid, now)
		if valid {
			base = activeFromCache(base, current, c.renewalTime(current.Exp, now))
			c.opts.Coordinator.Update(base)
		}
	}

	retry := initialEntitlementRetry
	for {
		delay, terminal := c.renew(ctx, &base, &current, valid)
		valid = base.CanDial() && base.Readiness != RelayReadinessSelfHosted
		if ctx.Err() != nil {
			return
		}
		if delay == 0 {
			delay = retry
			if retry < maximumEntitlementRetry {
				retry = time.Duration(math.Min(float64(maximumEntitlementRetry), float64(retry*2)))
			}
		} else if terminal {
			retry = initialEntitlementRetry
		} else {
			retry = initialEntitlementRetry
		}
		if base.Readiness == RelayReadinessRenewPending {
			untilExpiry := base.ExpiresAt.Sub(c.opts.Clock.Now())
			if untilExpiry < delay {
				delay = untilExpiry
			}
			if delay < 0 {
				delay = 0
			}
		}
		timer := c.opts.Clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-c.triggers:
			timer.Stop()
		case <-timer.C():
		}
	}
}

// renew returns zero for an exponentially backed-off transient failure.
func (c *EntitlementController) renew(ctx context.Context, base *ResolvedRelay, current *relay.CachedEntitlement, hadValid bool) (time.Duration, bool) {
	now := c.opts.Clock.Now()
	license, err := c.opts.Licenses.Load(ctx)
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		publishTerminal(c.opts.Coordinator, base, RelayReadinessNeedsLicense)
		return terminalEntitlementRetry, true
	}
	if errors.Is(err, ErrLicenseStoreUnavailable) {
		publishUnavailable(c.opts.Coordinator, base, now)
		return 0, false
	}
	if err != nil {
		if hadValid && current.ValidAt(relay.SessionSID(base.SessionID), now) {
			publishPending(c.opts.Coordinator, base, *current)
			return 0, false
		}
		publishUnavailable(c.opts.Coordinator, base, now)
		return 0, false
	}

	issued, err := c.opts.Issuer.Entitlement(ctx, license, relay.SessionSID(base.SessionID), base.Label, now)
	if err != nil {
		var issuerErr *relay.IssuerError
		if errors.As(err, &issuerErr) {
			switch issuerErr.Kind {
			case relay.IssuerInvalidKey:
				publishTerminal(c.opts.Coordinator, base, RelayReadinessInvalidKey)
				return terminalEntitlementRetry, true
			case relay.IssuerLapsed:
				publishTerminal(c.opts.Coordinator, base, RelayReadinessLapsed)
				return terminalEntitlementRetry, true
			case relay.IssuerNoSeat:
				publishNoSeat(c.opts.Coordinator, base, issuerErr.Activations)
				return terminalEntitlementRetry, true
			}
		}
		if hadValid && current.ValidAt(relay.SessionSID(base.SessionID), now) {
			publishPending(c.opts.Coordinator, base, *current)
			return 0, false
		}
		publishUnavailable(c.opts.Coordinator, base, now)
		return 0, false
	}

	next := relay.CachedEntitlement{
		Token: issued.Token, Exp: issued.Exp, ObtainedAt: now.Unix(),
		SID: relay.SessionSID(base.SessionID), MaxClients: issued.MaxClients,
	}
	// A cache failure must not throw away a freshly validated in-memory token.
	_ = c.opts.Cache.Save(next)
	refreshErr := c.opts.Refresh(ctx, c.opts.RelayHTTP, base.URL, base.SessionID, issued.Token)
	var refresh *relay.RelayRefreshError
	if errors.As(refreshErr, &refresh) && refresh.Kind == relay.RelayRefreshNoHost {
		base.ReconnectGeneration++
		refreshErr = nil
	}
	*current = next
	*base = activeFromIssued(*base, next, issued, c.renewalTime(next.Exp, now))
	c.opts.Coordinator.Update(*base)
	if refreshErr != nil {
		// Keep the newly issued token usable, but retry promptly so the live
		// relay alarm cannot remain pinned to the older token's expiry.
		return 0, false
	}
	return base.RenewsAt.Sub(now), false
}

func activeFromCache(base ResolvedRelay, cached relay.CachedEntitlement, renewsAt time.Time) ResolvedRelay {
	base.Readiness = RelayReadinessActive
	base.Dial = true
	base.EntitlementToken = NewRelayEntitlementToken(cached.Token.Value())
	base.RenewsAt = renewsAt
	base.ExpiresAt = time.Time{}
	base.UnavailableSince = time.Time{}
	base.MaxClients = cached.MaxClients
	return base
}

func activeFromIssued(base ResolvedRelay, cached relay.CachedEntitlement, issued relay.Entitlement, renewsAt time.Time) ResolvedRelay {
	base = activeFromCache(base, cached, renewsAt)
	base.Seats, base.SeatsUsed = issued.Seats, issued.SeatsUsed
	base.ActivationCount = 0
	base.ActivationSummaries = [maxRelayActivationSummaries]RelayActivationSummary{}
	return base
}

func (c *EntitlementController) renewalTime(exp int64, now time.Time) time.Time {
	remaining := time.Unix(exp, 0).Sub(now)
	half := remaining / 2
	random := c.opts.Jitter()
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	return now.Add(half - time.Duration(float64(half)*0.10*random))
}

func publishTerminal(coordinator *RelayCoordinator, base *ResolvedRelay, state RelayReadiness) {
	base.Readiness, base.Dial = state, false
	base.EntitlementToken = RelayEntitlementToken{}
	base.RenewsAt, base.ExpiresAt, base.UnavailableSince = time.Time{}, time.Time{}, time.Time{}
	base.ActivationCount = 0
	base.ActivationSummaries = [maxRelayActivationSummaries]RelayActivationSummary{}
	coordinator.Update(*base)
}

func publishUnavailable(coordinator *RelayCoordinator, base *ResolvedRelay, now time.Time) {
	if base.Readiness != RelayReadinessUnavailable || base.UnavailableSince.IsZero() {
		base.UnavailableSince = now
	}
	base.Readiness, base.Dial = RelayReadinessUnavailable, false
	base.EntitlementToken = RelayEntitlementToken{}
	base.RenewsAt, base.ExpiresAt = time.Time{}, time.Time{}
	coordinator.Update(*base)
}

func publishPending(coordinator *RelayCoordinator, base *ResolvedRelay, cached relay.CachedEntitlement) {
	*base = activeFromCache(*base, cached, time.Time{})
	base.Readiness = RelayReadinessRenewPending
	base.ExpiresAt = time.Unix(cached.Exp, 0)
	coordinator.Update(*base)
}

func publishNoSeat(coordinator *RelayCoordinator, base *ResolvedRelay, activations []relay.ActivationSummary) {
	base.Readiness, base.Dial = RelayReadinessNoSeat, false
	base.EntitlementToken = RelayEntitlementToken{}
	base.RenewsAt, base.ExpiresAt, base.UnavailableSince = time.Time{}, time.Time{}, time.Time{}
	base.ActivationSummaries = [maxRelayActivationSummaries]RelayActivationSummary{}
	base.ActivationCount = len(activations)
	if base.ActivationCount > len(base.ActivationSummaries) {
		base.ActivationCount = len(base.ActivationSummaries)
	}
	for i := 0; i < base.ActivationCount; i++ {
		base.ActivationSummaries[i] = RelayActivationSummary{Label: activations[i].Label, FirstSeen: activations[i].FirstSeen}
	}
	coordinator.Update(*base)
}
