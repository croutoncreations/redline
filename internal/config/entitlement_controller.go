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
	SaveContext(context.Context, relay.CachedEntitlement) error
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
	// BeforeCommit is a deterministic test seam immediately before an issuer
	// result is committed to runtime authority. Production leaves it nil.
	BeforeCommit func()
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

type entitlementPersistenceRequest struct {
	cached     relay.CachedEntitlement
	generation uint64
	version    uint64
}

type entitlementPersistenceResult struct {
	request entitlementPersistenceRequest
	err     error
}

type entitlementPersistenceWorker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests chan entitlementPersistenceRequest
	results  chan entitlementPersistenceResult
	done     chan struct{}

	latest     entitlementPersistenceRequest
	needsRetry bool
}

func newEntitlementPersistenceWorker(parent context.Context, cache entitlementCache) *entitlementPersistenceWorker {
	ctx, cancel := context.WithCancel(parent)
	worker := &entitlementPersistenceWorker{
		ctx: ctx, cancel: cancel,
		requests: make(chan entitlementPersistenceRequest, 1),
		results:  make(chan entitlementPersistenceResult, 1),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(worker.done)
		for {
			select {
			case <-ctx.Done():
				return
			case request := <-worker.requests:
				err := cache.SaveContext(ctx, request.cached)
				select {
				case worker.results <- entitlementPersistenceResult{request: request, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return worker
}

// submit is a versioned latest-value mailbox. It never waits for a blocked
// SaveContext, and any queued obsolete value is replaced by the newest token.
func (w *entitlementPersistenceWorker) submit(cached relay.CachedEntitlement, generation uint64) {
	w.latest = entitlementPersistenceRequest{cached: cached, generation: generation, version: w.latest.version + 1}
	w.needsRetry = false
	select {
	case w.requests <- w.latest:
		return
	default:
	}
	select {
	case <-w.requests:
	default:
	}
	select {
	case w.requests <- w.latest:
	default:
	}
}

func (w *entitlementPersistenceWorker) retry(generation uint64) {
	if !w.needsRetry || w.latest.generation != generation {
		return
	}
	w.submit(w.latest.cached, generation)
}

func (w *entitlementPersistenceWorker) stop() {
	w.cancel()
	<-w.done
}

// EntitlementController is the sole writer of hosted runtime entitlement
// state. Its public triggers are narrow lifecycle seams for the future local
// management API; this phase intentionally does not expose that API or UI.
type EntitlementController struct {
	opts      EntitlementControllerOptions
	triggers  chan entitlementTrigger
	mu        sync.Mutex
	triggerMu sync.Mutex
	running   bool

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

// TriggerLicenseChanged supersedes work and runtime authority for the previous
// credential generation. Credential replacement and its validation remain the
// responsibility of the later management API, not this controller.
func (c *EntitlementController) TriggerLicenseChanged() {
	c.mu.Lock()
	c.credentialGeneration++
	generation := c.credentialGeneration
	cancel := c.attemptCancel
	// Replacing a credential synchronously revokes authority established by the
	// prior generation. The event loop may be inside issuer or refresh work, so
	// waiting for it to observe a trigger would leave an obsolete token dialable.
	next := c.opts.Coordinator.Current()
	publishUnavailable(c.opts.Coordinator, &next, c.opts.Clock.Now())
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
	// A one-slot latest trigger mailbox is sufficient because either trigger
	// starts renewal. Replacing rather than dropping the occupied slot keeps a
	// fresh generation's 402/1008 or license event when it races an older event.
	c.triggerMu.Lock()
	defer c.triggerMu.Unlock()
	select {
	case c.triggers <- trigger:
		return
	default:
	}
	select {
	case <-c.triggers:
	default:
	}
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
	startupGeneration := c.generation()
	var current relay.CachedEntitlement
	var valid bool
	// Resolver has already established that Keychain was readable and held a
	// key before a cache may make the hosted relay usable. In particular, never
	// publish a brief active state when startup resolution said unavailable.
	if base.Readiness == RelayReadinessHostedConfigured || base.Readiness == RelayReadinessActive || base.Readiness == RelayReadinessRenewPending {
		current, valid, _ = c.opts.Cache.Load(sid, now)
		if valid {
			c.mu.Lock()
			now = c.opts.Clock.Now()
			valid = startupGeneration == c.credentialGeneration && current.ValidAt(sid, now)
			if valid {
				base = activeFromCache(base, current, c.renewalTime(current.Exp, now))
				c.opts.Coordinator.Update(base)
			}
			c.mu.Unlock()
		}
	}

	retry := initialEntitlementRetry
	authorityGeneration := startupGeneration
	persistence := newEntitlementPersistenceWorker(ctx, c.opts.Cache)
	defer persistence.stop()
	for {
		generation := c.generation()
		if authorityGeneration != generation {
			current = relay.CachedEntitlement{}
			valid = false
			base = c.opts.Coordinator.Current()
			base.PersistenceDegraded = false
		}
		persistence.retry(generation)
		attemptCtx, cancelAttempt := c.startAttempt(ctx, generation)
		delay, terminal := c.renew(attemptCtx, generation, &base, &current, &authorityGeneration, persistence)
		c.finishAttempt(cancelAttempt)
		now = c.opts.Clock.Now()
		valid = authorityGeneration == generation && current.ValidAt(sid, now)
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
		// Re-sample immediately before sleeping. Issuer, refresh, and result
		// handling may all have crossed the raw signed expiration boundary.
		now = c.opts.Clock.Now()
		valid = authorityGeneration == generation && current.ValidAt(sid, now)
		if base.CanDial() && !valid {
			publishUnavailable(c.opts.Coordinator, &base, now)
		}
		if valid && base.CanDial() {
			untilExpiry := time.Unix(current.Exp, 0).Sub(now)
			if untilExpiry < delay {
				delay = untilExpiry
			}
		}
		if delay <= 0 {
			delay = initialEntitlementRetry
		}
		timer := c.opts.Clock.NewTimer(delay)
	waitForRenewal:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case result := <-persistence.results:
				c.applyPersistenceResult(generation, &base, &current, authorityGeneration, persistence, result)
			case trigger := <-c.triggers:
				timer.Stop()
				if trigger.generation == c.generation() && (trigger.reason == triggerLicenseChanged || trigger.reason == triggerRelayRequired) {
					retry = initialEntitlementRetry
				}
				break waitForRenewal
			case <-timer.C():
				break waitForRenewal
			}
		}
	}
}

// renew returns zero for an exponentially backed-off transient failure.
func (c *EntitlementController) renew(ctx context.Context, generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, persistence *entitlementPersistenceWorker) (time.Duration, bool) {
	now := c.opts.Clock.Now()
	sid := relay.SessionSID(base.SessionID)
	hadValid := *authorityGeneration == generation && current.ValidAt(sid, now)
	if base.CanDial() && !hadValid {
		// Validation skew never extends runtime authority. Publish the raw signed
		// expiration boundary before attempting any renewal I/O.
		publishUnavailable(c.opts.Coordinator, base, now)
	}

	license, err := c.opts.Licenses.Load(ctx)
	if ctx.Err() != nil {
		return 0, false
	}
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		if c.publishTerminalForGeneration(generation, base, current, authorityGeneration, RelayReadinessNeedsLicense, nil) {
			return terminalEntitlementRetry, true
		}
		return 0, false
	}
	if err != nil {
		// Re-sample after Keychain work; a token valid at entry may have expired
		// while the credential store was blocked.
		c.publishFallbackForGeneration(generation, base, current, *authorityGeneration)
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
			var state RelayReadiness
			switch issuerErr.Kind {
			case relay.IssuerInvalidKey:
				state = RelayReadinessInvalidKey
			case relay.IssuerLapsed:
				state = RelayReadinessLapsed
			case relay.IssuerNoSeat:
				state = RelayReadinessNoSeat
			}
			if state != "" {
				if c.publishTerminalForGeneration(generation, base, current, authorityGeneration, state, issuerErr.Activations) {
					return terminalEntitlementRetry, true
				}
				return 0, false
			}
		}
		c.publishFallbackForGeneration(generation, base, current, *authorityGeneration)
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
		c.publishFallbackForGeneration(generation, base, current, *authorityGeneration)
		return 0, false
	}
	if c.opts.BeforeCommit != nil {
		c.opts.BeforeCommit()
	}
	if !c.commitIssued(generation, base, current, authorityGeneration, persistence, next, issued.Entitlement) {
		return 0, false
	}
	return base.RenewsAt.Sub(c.opts.Clock.Now()), false
}

func (c *EntitlementController) publishFallbackForGeneration(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	now := c.opts.Clock.Now()
	sid := relay.SessionSID(base.SessionID)
	if authorityGeneration == generation && current.ValidAt(sid, now) {
		publishPending(c.opts.Coordinator, base, *current)
	} else {
		publishUnavailable(c.opts.Coordinator, base, now)
	}
	return true
}

func (c *EntitlementController) publishTerminalForGeneration(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, state RelayReadiness, activations []relay.ActivationSummary) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	// A terminal issuer decision revokes fallback eligibility for the complete
	// credential generation, not merely for this attempt.
	*current = relay.CachedEntitlement{}
	*authorityGeneration = generation
	if state == RelayReadinessNoSeat {
		publishNoSeat(c.opts.Coordinator, base, activations)
	} else {
		publishTerminal(c.opts.Coordinator, base, state)
	}
	return true
}

func (c *EntitlementController) commitIssued(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, next relay.CachedEntitlement, issued relay.Entitlement) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	// Validation skew is issuer-response validation only. Runtime publication
	// uses a fresh desktop sample and requires the raw signed exp to be future.
	now := c.opts.Clock.Now()
	if !time.Unix(next.Exp, 0).After(now) {
		*current = relay.CachedEntitlement{}
		*authorityGeneration = generation
		publishUnavailable(c.opts.Coordinator, base, now)
		return false
	}
	c.relayTriggerPending = false
	*current = next
	*authorityGeneration = generation
	*base = activeFromIssued(*base, next, issued, c.renewalTime(next.Exp, now))
	// Pending durability is observable immediately, and SaveContext runs only in
	// the worker. Neither flock nor fsync can stall this authority commit.
	base.PersistenceDegraded = true
	persistence.submit(next, generation)
	c.opts.Coordinator.Update(*base)
	return true
}

func (c *EntitlementController) applyPersistenceResult(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration uint64, persistence *entitlementPersistenceWorker, result entitlementPersistenceResult) {
	if result.request.version != persistence.latest.version || result.request.generation != generation || generation != c.generation() {
		return
	}
	if result.err != nil {
		persistence.needsRetry = true
		return
	}
	persistence.needsRetry = false
	if !base.PersistenceDegraded {
		return
	}
	base.PersistenceDegraded = false
	// Clearing a persistence warning is still an active-state publication, so
	// guard it with a fresh raw-expiration sample.
	now := c.opts.Clock.Now()
	if authorityGeneration != generation || !current.ValidAt(relay.SessionSID(base.SessionID), now) {
		publishUnavailable(c.opts.Coordinator, base, now)
		return
	}
	c.opts.Coordinator.Update(*base)
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
	degraded := base.PersistenceDegraded
	clearRelayStatus(base)
	base.Readiness = RelayReadinessUnavailable
	base.UnavailableSince = since
	base.PersistenceDegraded = degraded
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
