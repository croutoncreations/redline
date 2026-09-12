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
	Entitlement(context.Context, string, string, string) (relay.ReceivedEntitlement, error)
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

type entitlementTriggerReason string

const (
	triggerLicenseChanged entitlementTriggerReason = "license_changed"
	triggerRelayRequired  entitlementTriggerReason = "relay_entitlement_required"
)

type entitlementTrigger struct {
	reason     entitlementTriggerReason
	generation uint64
}

type pendingEntitlementPersistence struct {
	cached     relay.CachedEntitlement
	generation uint64
}

// EntitlementController is the sole writer of hosted runtime entitlement
// state. Its public triggers are narrow lifecycle seams for the future local
// management API; this phase intentionally does not expose that API or UI.
type EntitlementController struct {
	opts     EntitlementControllerOptions
	triggers chan entitlementTrigger
	mu       sync.Mutex
	running  bool

	credentialGeneration uint64
	attemptCancel        context.CancelFunc
	relayTriggerPending  bool
	relayTriggerGen      uint64
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
	return &EntitlementController{opts: opts, triggers: make(chan entitlementTrigger, 1)}
}

// TriggerLicenseChanged supersedes work for the previous credential
// generation. It is the only controller method a future license-change API
// needs; persistence and input validation remain outside this phase.
func (c *EntitlementController) TriggerLicenseChanged() {
	c.mu.Lock()
	c.credentialGeneration++
	generation := c.credentialGeneration
	cancel := c.attemptCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.enqueue(entitlementTrigger{reason: triggerLicenseChanged, generation: generation})
}

// TriggerRelayEntitlement handles the dialer's authoritative 402/1008 typed
// contract. Repeated signals for one credential generation coalesce, so a
// hostile relay cannot turn the controller's scheduled backoff into a hot loop.
func (c *EntitlementController) TriggerRelayEntitlement(_ relay.EntitlementSignal) {
	c.mu.Lock()
	generation := c.credentialGeneration
	if c.relayTriggerPending && c.relayTriggerGen == generation {
		c.mu.Unlock()
		return
	}
	c.relayTriggerPending, c.relayTriggerGen = true, generation
	cancel := c.attemptCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.enqueue(entitlementTrigger{reason: triggerRelayRequired, generation: generation})
}

// Trigger remains a compatibility alias for an authoritative relay event.
func (c *EntitlementController) Trigger() {
	c.TriggerRelayEntitlement(relay.EntitlementHandshakeRequired)
}

func (c *EntitlementController) enqueue(trigger entitlementTrigger) {
	select {
	case c.triggers <- trigger:
	default:
	}
}

func (c *EntitlementController) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credentialGeneration
}

func (c *EntitlementController) startAttempt(parent context.Context, generation uint64) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if generation != c.credentialGeneration {
		cancel()
	} else {
		c.attemptCancel = cancel
	}
	c.mu.Unlock()
	return ctx, cancel
}

func (c *EntitlementController) finishAttempt(cancel context.CancelFunc) {
	c.mu.Lock()
	c.attemptCancel = nil
	c.mu.Unlock()
	cancel()
}

func (c *EntitlementController) accepted(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	// Do not drain the trigger channel here. A relay failure racing acceptance
	// may describe the newly accepted handshake authority; one redundant
	// renewal is safer than losing a fresh 402/1008 signal.
	c.relayTriggerPending = false
	return true
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
		clearRelayStatus(&base)
		base.Readiness = RelayReadinessOff
		c.opts.Coordinator.Update(base)
		<-ctx.Done()
		return
	case RelayModeSelfHosted:
		clearRelayStatus(&base)
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
	var pendingPersistence *pendingEntitlementPersistence
	for {
		generation := c.generation()
		if pendingPersistence != nil && pendingPersistence.generation != generation {
			// Credential replacement supersedes durability work for authority
			// accepted under the previous license generation.
			pendingPersistence = nil
			if base.PersistenceDegraded {
				base.PersistenceDegraded = false
				c.opts.Coordinator.Update(base)
			}
		}
		attemptCtx, cancelAttempt := c.startAttempt(ctx, generation)
		delay, terminal := c.renew(attemptCtx, generation, &base, &current, valid, &pendingPersistence)
		c.finishAttempt(cancelAttempt)
		valid = current.ValidAt(sid, c.opts.Clock.Now())
		if ctx.Err() != nil {
			return
		}
		if generation != c.generation() {
			retry = initialEntitlementRetry
			continue
		}
		if delay == 0 {
			delay = retry
			if retry < maximumEntitlementRetry {
				retry = time.Duration(math.Min(float64(maximumEntitlementRetry), float64(retry*2)))
			}
		} else {
			retry = initialEntitlementRetry
		}
		if terminal {
			retry = initialEntitlementRetry
		}
		if valid && base.CanDial() {
			untilExpiry := time.Unix(current.Exp, 0).Sub(c.opts.Clock.Now())
			if untilExpiry < delay {
				delay = untilExpiry
			}
		}
		// No failure path may install a zero-duration timer. At raw expiration
		// renew publishes unavailable, then retains ordinary retry backoff.
		if delay <= 0 {
			delay = initialEntitlementRetry
		}
		timer := c.opts.Clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case trigger := <-c.triggers:
			timer.Stop()
			if trigger.generation == c.generation() && (trigger.reason == triggerLicenseChanged || trigger.reason == triggerRelayRequired) {
				retry = initialEntitlementRetry
			}
		case <-timer.C():
		}
	}
}

// renew returns zero for an exponentially backed-off transient failure.
func (c *EntitlementController) renew(ctx context.Context, generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, hadValid bool, pendingPersistence **pendingEntitlementPersistence) (time.Duration, bool) {
	now := c.opts.Clock.Now()
	sid := relay.SessionSID(base.SessionID)
	defer func() {
		degraded := *pendingPersistence != nil
		if base.PersistenceDegraded != degraded {
			base.PersistenceDegraded = degraded
			c.opts.Coordinator.Update(*base)
		}
	}()
	if hadValid && !current.ValidAt(sid, now) {
		// Validation skew never extends runtime authority. Publish the raw signed
		// expiration boundary before attempting any persistence or renewal I/O.
		publishUnavailable(c.opts.Coordinator, base, now)
		hadValid = false
	}
	if *pendingPersistence != nil {
		if err := c.opts.Cache.Save((*pendingPersistence).cached); err != nil {
			if !base.PersistenceDegraded {
				base.PersistenceDegraded = true
				c.opts.Coordinator.Update(*base)
			}
		} else {
			*pendingPersistence = nil
			base.PersistenceDegraded = false
			c.opts.Coordinator.Update(*base)
		}
	}

	license, err := c.opts.Licenses.Load(ctx)
	if ctx.Err() != nil {
		return 0, false
	}
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		publishTerminal(c.opts.Coordinator, base, RelayReadinessNeedsLicense)
		return terminalEntitlementRetry, true
	}
	if err != nil {
		// Once this controller has established trusted in-memory state, any
		// transient Keychain failure is a renewal problem, not authority to
		// discard that state. Startup still remains fail closed because hadValid
		// is false until this run has loaded or accepted a valid token.
		if hadValid && current.ValidAt(sid, now) {
			publishPending(c.opts.Coordinator, base, *current)
			return 0, false
		}
		publishUnavailable(c.opts.Coordinator, base, now)
		return 0, false
	}

	issued, err := c.opts.Issuer.Entitlement(ctx, license, sid, base.Label)
	// Tokens and the protected cache use Unix seconds. Canonicalize the receipt
	// sampled by IssuerClient after complete response/body handling, then use it
	// for validation, obtained_at, and renewal scheduling.
	receivedAt := issued.ReceivedAt.UTC().Truncate(time.Second)
	if issued.ReceivedAt.IsZero() {
		// Deterministic issuer fakes may omit the receipt, but production
		// IssuerClient always supplies its post-response clock sample.
		receivedAt = c.opts.Clock.Now().UTC().Truncate(time.Second)
	}
	if ctx.Err() != nil || generation != c.generation() {
		return 0, false
	}
	if err == nil {
		if validationErr := relay.ValidateEntitlementAt(issued.Entitlement, sid, receivedAt); validationErr != nil {
			err = &relay.IssuerError{Kind: relay.IssuerInvalidResponse}
		}
	}
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
		if hadValid && current.ValidAt(sid, receivedAt) {
			publishPending(c.opts.Coordinator, base, *current)
			return 0, false
		}
		publishUnavailable(c.opts.Coordinator, base, receivedAt)
		return 0, false
	}

	next := relay.CachedEntitlement{
		Token: issued.Token, Exp: issued.Exp, ObtainedAt: receivedAt.Unix(),
		SID: sid, MaxClients: issued.MaxClients,
	}
	refreshErr := c.opts.Refresh(ctx, c.opts.RelayHTTP, base.URL, base.SessionID, issued.Token)
	if ctx.Err() != nil || generation != c.generation() {
		return 0, false
	}
	var refresh *relay.RelayRefreshError
	acceptedByRelay := refreshErr == nil
	if errors.As(refreshErr, &refresh) && refresh.Kind == relay.RelayRefreshNoHost {
		// 423 is returned only after token verification when no host socket is
		// present, so acceptance is authoritative and the supervisor reconnects.
		base.ReconnectGeneration++
		acceptedByRelay = true
	}
	if !acceptedByRelay {
		// Auth/rejection and transport failures establish no new authority. Never
		// cache or publish the rejected token; preserve the previous valid token.
		if hadValid && current.ValidAt(sid, receivedAt) {
			publishPending(c.opts.Coordinator, base, *current)
		} else {
			publishUnavailable(c.opts.Coordinator, base, receivedAt)
		}
		return 0, false
	}
	if !c.accepted(generation) {
		return 0, false
	}
	*current = next
	*base = activeFromIssued(*base, next, issued.Entitlement, c.renewalTime(next.Exp, receivedAt))
	// Publish relay-accepted in-memory authority before potentially slow durable
	// I/O. A reconnect racing a blocked fsync must already see the new token.
	c.opts.Coordinator.Update(*base)
	// A newly accepted credential supersedes any older pending cache write,
	// including one from the same license generation.
	*pendingPersistence = nil
	if err := c.opts.Cache.Save(next); err != nil {
		*pendingPersistence = &pendingEntitlementPersistence{cached: next, generation: generation}
		base.PersistenceDegraded = true
		c.opts.Coordinator.Update(*base)
	}
	if *pendingPersistence != nil {
		return 0, false
	}
	return base.RenewsAt.Sub(receivedAt), false
}

func clearRelayStatus(base *ResolvedRelay) {
	base.Dial = false
	base.EntitlementToken = RelayEntitlementToken{}
	base.RenewsAt, base.ExpiresAt, base.UnavailableSince = time.Time{}, time.Time{}, time.Time{}
	base.Seats, base.SeatsUsed, base.MaxClients = 0, 0, 0
	base.PersistenceDegraded = false
	base.ActivationCount = 0
	base.ActivationSummaries = [maxRelayActivationSummaries]RelayActivationSummary{}
}

func activeFromCache(base ResolvedRelay, cached relay.CachedEntitlement, renewsAt time.Time) ResolvedRelay {
	clearRelayStatus(&base)
	base.Readiness = RelayReadinessActive
	base.Dial = true
	base.EntitlementToken = NewRelayEntitlementToken(cached.Token.Value())
	base.RenewsAt = renewsAt
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
	clearRelayStatus(base)
	base.Readiness = state
	coordinator.Update(*base)
}

func publishUnavailable(coordinator *RelayCoordinator, base *ResolvedRelay, now time.Time) {
	since := base.UnavailableSince
	if base.Readiness != RelayReadinessUnavailable || since.IsZero() {
		since = now
	}
	clearRelayStatus(base)
	base.Readiness = RelayReadinessUnavailable
	base.UnavailableSince = since
	coordinator.Update(*base)
}

func publishPending(coordinator *RelayCoordinator, base *ResolvedRelay, cached relay.CachedEntitlement) {
	*base = activeFromCache(*base, cached, time.Time{})
	base.Readiness = RelayReadinessRenewPending
	base.ExpiresAt = time.Unix(cached.Exp, 0)
	coordinator.Update(*base)
}

func publishNoSeat(coordinator *RelayCoordinator, base *ResolvedRelay, activations []relay.ActivationSummary) {
	clearRelayStatus(base)
	base.Readiness = RelayReadinessNoSeat
	base.ActivationCount = len(activations)
	if base.ActivationCount > len(base.ActivationSummaries) {
		base.ActivationCount = len(base.ActivationSummaries)
	}
	for i := 0; i < base.ActivationCount; i++ {
		base.ActivationSummaries[i] = RelayActivationSummary{Label: activations[i].Label, FirstSeen: activations[i].FirstSeen}
	}
	coordinator.Update(*base)
}
