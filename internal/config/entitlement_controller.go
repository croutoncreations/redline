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
	initialEntitlementRetry        = time.Minute
	maximumEntitlementRetry        = 6 * time.Hour
	terminalEntitlementRetry       = 6 * time.Hour
	defaultPersistenceDrainTimeout = 2 * time.Second
)

type entitlementIssuer interface {
	Entitlement(context.Context, string, string, string) (relay.ReceivedEntitlement, error)
}

type entitlementCache interface {
	Load(string, string, time.Time) (relay.CachedEntitlement, bool, error)
	SaveContext(context.Context, relay.CachedEntitlement) error
}

type entitlementRevocations interface {
	Load() (relay.EntitlementRevocation, bool, error)
	SaveContext(context.Context, relay.EntitlementRevocation) error
}

type noopEntitlementRevocations struct{}

func (noopEntitlementRevocations) Load() (relay.EntitlementRevocation, bool, error) {
	return relay.EntitlementRevocation{}, false, nil
}
func (noopEntitlementRevocations) SaveContext(context.Context, relay.EntitlementRevocation) error {
	return nil
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
	Revocations entitlementRevocations
	RelayHTTP   *http.Client
	Clock       entitlementClock
	ExpiryClock entitlementClock
	Jitter      func() float64
	// PersistenceDrainTimeout bounds persistence-worker shutdown, including
	// terminal revocation-marker durability. The barrier is independent of parent
	// cancellation, but cannot cancel an OS syscall already in progress.
	PersistenceDrainTimeout time.Duration
	Refresh                 func(context.Context, *http.Client, string, string, relay.Secret) error
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

	latest           entitlementPersistenceRequest
	completedVersion uint64
	needsRetry       bool
}

func newEntitlementPersistenceWorker(cache entitlementCache) *entitlementPersistenceWorker {
	// Best-effort authority caching is canceled explicitly by shutdown. It is
	// not parent-bound so cancellation ordering remains separate from the
	// high-priority revocation marker barrier.
	ctx, cancel := context.WithCancel(context.Background())
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

func (w *entitlementPersistenceWorker) discard() {
	w.latest = entitlementPersistenceRequest{version: w.latest.version + 1}
	w.needsRetry = false
}

func (w *entitlementPersistenceWorker) shutdown(parent context.Context, timeout time.Duration) *entitlementPersistenceResult {
	// The barrier intentionally survives parent cancellation. Its deadline also
	// bounds waiting for worker exit: SaveContext normally observes cancellation,
	// but a filesystem sync already inside the kernel cannot be canceled. In that
	// case the goroutine is unavoidably orphaned until the syscall returns or the
	// process exits. Channels stay open so its eventual return cannot panic.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	stopAndWait := func() {
		w.cancel()
		select {
		case <-w.done:
		case <-drainCtx.Done():
		}
	}

	if !w.latest.cached.Revoked {
		stopAndWait()
		return nil
	}
	if w.needsRetry {
		w.retry(w.latest.generation)
	}
	targetVersion := w.latest.version
	if w.completedVersion >= targetVersion {
		stopAndWait()
		return nil
	}

	for {
		select {
		case result := <-w.results:
			if result.request.version != targetVersion {
				continue
			}
			stopAndWait()
			return &result
		case <-drainCtx.Done():
			w.cancel()
			return nil
		case <-w.done:
			return nil
		}
	}
}

type entitlementRevocationRequest struct {
	marker     relay.EntitlementRevocation
	generation uint64
	version    uint64
}

type entitlementRevocationResult struct {
	request entitlementRevocationRequest
	err     error
}

type entitlementRevocationWorker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests chan entitlementRevocationRequest
	results  chan entitlementRevocationResult
	done     chan struct{}

	latest           entitlementRevocationRequest
	completedVersion uint64
	needsRetry       bool
}

func newEntitlementRevocationWorker(store entitlementRevocations) *entitlementRevocationWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &entitlementRevocationWorker{
		ctx: ctx, cancel: cancel,
		requests: make(chan entitlementRevocationRequest, 1),
		results:  make(chan entitlementRevocationResult, 1),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(worker.done)
		for {
			select {
			case <-ctx.Done():
				return
			case request := <-worker.requests:
				err := store.SaveContext(ctx, request.marker)
				select {
				case worker.results <- entitlementRevocationResult{request: request, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return worker
}

func (w *entitlementRevocationWorker) submit(request entitlementRevocationRequest) {
	request.version = w.latest.version + 1
	w.latest = request
	w.needsRetry = false
	select {
	case w.requests <- request:
		return
	default:
	}
	select {
	case <-w.requests:
	default:
	}
	select {
	case w.requests <- request:
	default:
	}
}

func (w *entitlementRevocationWorker) save(marker relay.EntitlementRevocation, generation uint64) {
	w.submit(entitlementRevocationRequest{marker: marker, generation: generation})
}

func (w *entitlementRevocationWorker) retry(generation uint64) {
	if !w.needsRetry || w.latest.generation != generation {
		return
	}
	request := w.latest
	w.submit(request)
}

// shutdown is the terminal marker durability barrier. It consumes and retries
// a failed result even when Run returned before observing that buffered result.
func (w *entitlementRevocationWorker) shutdown(parent context.Context, timeout time.Duration) *entitlementRevocationResult {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	stopAndWait := func() {
		w.cancel()
		select {
		case <-w.done:
		case <-drainCtx.Done():
		}
	}
	if w.latest.version == 0 {
		stopAndWait()
		return nil
	}
	targetVersion := w.latest.version
	if w.completedVersion >= targetVersion && !w.needsRetry {
		stopAndWait()
		return nil
	}
	if w.needsRetry {
		w.retry(w.latest.generation)
		targetVersion = w.latest.version
	}
	for {
		select {
		case result := <-w.results:
			if result.request.version != targetVersion {
				continue
			}
			if result.err != nil {
				w.needsRetry = true
				w.retry(result.request.generation)
				targetVersion = w.latest.version
				continue
			}
			stopAndWait()
			return &result
		case <-drainCtx.Done():
			w.cancel()
			return nil
		case <-w.done:
			return nil
		}
	}
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
	runCtx    context.Context

	credentialGeneration uint64
	attemptCancel        context.CancelFunc
	relayEventSequence   uint64
	relayAttemptSequence uint64
	relaySignalQueued    bool
	relayFollowupQueued  bool
	authorityEpoch       uint64
	expiryCancel         context.CancelFunc
	expiryWatchers       sync.WaitGroup
}

func NewEntitlementController(opts EntitlementControllerOptions) *EntitlementController {
	if opts.Clock == nil {
		opts.Clock = realEntitlementClock{}
	}
	if opts.ExpiryClock == nil {
		opts.ExpiryClock = realEntitlementClock{}
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Float64
	}
	if opts.Revocations == nil {
		opts.Revocations = noopEntitlementRevocations{}
	}
	if opts.PersistenceDrainTimeout <= 0 {
		opts.PersistenceDrainTimeout = defaultPersistenceDrainTimeout
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
	c.relayAttemptSequence = c.relayEventSequence
	c.relaySignalQueued = false
	c.relayFollowupQueued = false
	cancel := c.attemptCancel
	// Replacing a credential synchronously revokes authority established by the
	// prior generation. The event loop may be inside issuer or refresh work, so
	// waiting for it to observe a trigger would leave an obsolete token dialable.
	c.revokeAuthorityLocked(c.opts.Clock.Now())
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
	c.relayEventSequence++
	if c.relaySignalQueued {
		c.mu.Unlock()
		return
	}
	c.relaySignalQueued = true
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
	c.runCtx = ctx
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.expiryCancel != nil {
			c.expiryCancel()
			c.expiryCancel = nil
		}
		c.running = false
		c.runCtx = nil
		c.mu.Unlock()
		c.expiryWatchers.Wait()
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
	var credentialFingerprint string
	var valid bool
	// Keychain must be loaded before cache authority is considered. The cache is
	// cryptographically bound to that exact credential generation, so replacement
	// cannot briefly publish authority issued for an old key.
	license, licenseErr := c.opts.Licenses.Load(ctx)
	licenseLoaded := true
	if licenseErr == nil && license != "" {
		credentialFingerprint = relay.CredentialFingerprint(license)
	}
	now = c.opts.Clock.Now()
	if credentialFingerprint != "" && (base.Readiness == RelayReadinessHostedConfigured || base.Readiness == RelayReadinessActive || base.Readiness == RelayReadinessRenewPending) {
		current, valid, _ = c.opts.Cache.Load(sid, credentialFingerprint, now)
		// runServe admits only one service/controller process. Load the durable
		// floor after potentially blocking cache I/O, immediately before startup
		// publication, so a terminal marker written while Load was blocked wins.
		// Store locks keep supported inter-process writes monotonic; there is no
		// external post-startup mutation API that requires a watcher here.
		marker, markerExists, markerErr := c.opts.Revocations.Load()
		if markerErr != nil || (markerExists && marker.CredentialFingerprint == credentialFingerprint && current.ObtainedAt <= marker.RevokedAt) {
			current = relay.CachedEntitlement{}
			valid = false
		}
		if valid {
			c.mu.Lock()
			now = c.opts.Clock.Now()
			valid = startupGeneration == c.credentialGeneration && current.ValidAt(sid, now)
			if valid {
				base = activeFromCache(base, current, c.renewalTime(current.Exp, now))
				c.authorityEpoch++
				c.installExpiryWatcherLocked(ctx, startupGeneration, c.authorityEpoch, current.Exp)
				c.opts.Coordinator.Update(base)
			}
			c.mu.Unlock()
		}
	}

	retry := initialEntitlementRetry
	authorityGeneration := startupGeneration
	persistence := newEntitlementPersistenceWorker(c.opts.Cache)
	revocationPersistence := newEntitlementRevocationWorker(c.opts.Revocations)
	defer func() {
		// Cancel old authority I/O before entering the independent marker barrier.
		// Both workers share one shutdown budget, but blocked cache I/O cannot
		// consume time before the marker gets its durability opportunity.
		persistence.cancel()
		started := time.Now()
		if result := revocationPersistence.shutdown(ctx, c.opts.PersistenceDrainTimeout); result != nil {
			c.applyRevocationResult(result.request.generation, &base, &current, authorityGeneration, revocationPersistence, *result)
		}
		remaining := c.opts.PersistenceDrainTimeout - time.Since(started)
		if remaining < 0 {
			remaining = 0
		}
		_ = persistence.shutdown(ctx, remaining)
	}()
	for {
		generation := c.generation()
		if authorityGeneration != generation {
			current = relay.CachedEntitlement{}
			valid = false
			base = c.opts.Coordinator.Current()
			base.PersistenceDegraded = false
		}
		persistence.retry(generation)
		revocationPersistence.retry(generation)
		attemptCtx, cancelAttempt := c.startAttempt(ctx, generation)
		c.mu.Lock()
		attemptRelaySequence := c.relayAttemptSequence
		c.mu.Unlock()
		delay, terminal := c.renew(attemptCtx, generation, attemptRelaySequence, &license, &licenseErr, &licenseLoaded, &base, &current, &credentialFingerprint, &authorityGeneration, persistence, revocationPersistence)
		c.finishAttempt(cancelAttempt)
		c.mu.Lock()
		if !c.relayFollowupQueued {
			c.relaySignalQueued = false
		}
		c.mu.Unlock()
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
			case result := <-revocationPersistence.results:
				c.applyRevocationResult(generation, &base, &current, authorityGeneration, revocationPersistence, result)
			case trigger := <-c.triggers:
				timer.Stop()
				c.mu.Lock()
				if trigger.reason == triggerRelayRequired {
					c.relayAttemptSequence = c.relayEventSequence
					c.relayFollowupQueued = false
				}
				currentGeneration := c.credentialGeneration
				c.mu.Unlock()
				if trigger.generation == currentGeneration && (trigger.reason == triggerLicenseChanged || trigger.reason == triggerRelayRequired) {
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
func (c *EntitlementController) renew(ctx context.Context, generation, attemptRelaySequence uint64, licenseHint *string, licenseErrHint *error, licenseLoaded *bool, base *ResolvedRelay, current *relay.CachedEntitlement, credentialFingerprint *string, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, revocationPersistence *entitlementRevocationWorker) (time.Duration, bool) {
	now := c.opts.Clock.Now()
	sid := relay.SessionSID(base.SessionID)
	hadValid := *authorityGeneration == generation && current.ValidAt(sid, now)
	if base.CanDial() && !hadValid {
		// Validation skew never extends runtime authority. Publish the raw signed
		// expiration boundary before attempting any renewal I/O.
		publishUnavailable(c.opts.Coordinator, base, now)
	}

	var license string
	var err error
	if *licenseLoaded {
		license, err = *licenseHint, *licenseErrHint
		*licenseHint, *licenseErrHint, *licenseLoaded = "", nil, false
	} else {
		license, err = c.opts.Licenses.Load(ctx)
	}
	if ctx.Err() != nil {
		return 0, false
	}
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		if c.publishTerminalForGeneration(generation, base, current, credentialFingerprint, authorityGeneration, persistence, revocationPersistence, c.opts.Clock.Now(), RelayReadinessNeedsLicense, nil) {
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
	loadedFingerprint := relay.CredentialFingerprint(license)
	if *credentialFingerprint != "" && *credentialFingerprint != loadedFingerprint {
		c.mu.Lock()
		if generation == c.credentialGeneration {
			*current = relay.CachedEntitlement{}
			*authorityGeneration = generation
			c.revokeAuthorityLocked(c.opts.Clock.Now())
		}
		c.mu.Unlock()
	}
	*credentialFingerprint = loadedFingerprint

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
				if c.publishTerminalForGeneration(generation, base, current, credentialFingerprint, authorityGeneration, persistence, revocationPersistence, receivedAt, state, issuerErr.Activations) {
					return terminalEntitlementRetry, true
				}
				return 0, false
			}
		}
		c.publishFallbackForGeneration(generation, base, current, *authorityGeneration)
		return 0, false
	}

	next := relay.CachedEntitlement{
		SchemaVersion: relay.EntitlementCacheSchemaVersion, CredentialFingerprint: *credentialFingerprint,
		Token: issued.Token, Exp: issued.Exp, ObtainedAt: receivedAt.Unix(), SID: sid, MaxClients: issued.MaxClients,
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
	if !c.commitIssued(ctx, generation, attemptRelaySequence, base, current, authorityGeneration, persistence, next, issued.Entitlement) {
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

func (c *EntitlementController) publishTerminalForGeneration(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, credentialFingerprint *string, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, revocationPersistence *entitlementRevocationWorker, revokedAt time.Time, state RelayReadiness, activations []relay.ActivationSummary) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	revocationVersion := revokedAt.UTC().Truncate(time.Second).Unix()
	if current.ObtainedAt > revocationVersion {
		revocationVersion = current.ObtainedAt
	}
	*current = relay.CachedEntitlement{}
	*authorityGeneration = generation
	c.authorityEpoch++
	if c.expiryCancel != nil {
		c.expiryCancel()
		c.expiryCancel = nil
	}
	if state == RelayReadinessNoSeat {
		setNoSeat(base, activations)
	} else {
		setTerminal(base, state)
	}
	if *credentialFingerprint != "" && (state == RelayReadinessInvalidKey || state == RelayReadinessLapsed || state == RelayReadinessNoSeat) {
		// Revocation has a distinct path, lock, and worker. Discard ownership of
		// any cache request: an obsolete cache completion can remain visible, but
		// the matching marker always wins at restart.
		persistence.discard()
		marker := relay.EntitlementRevocation{
			SchemaVersion:         relay.EntitlementRevocationSchemaVersion,
			CredentialFingerprint: *credentialFingerprint,
			RevokedAt:             revocationVersion,
		}
		base.PersistenceDegraded = true
		revocationPersistence.save(marker, generation)
	}
	c.opts.Coordinator.Update(*base)
	return true
}

func (c *EntitlementController) commitIssued(parent context.Context, generation, attemptRelaySequence uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, next relay.CachedEntitlement, issued relay.Entitlement) bool {
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
	*current = next
	*authorityGeneration = generation
	*base = activeFromIssued(*base, next, issued, c.renewalTime(next.Exp, now))
	// Pending durability is observable immediately, and SaveContext runs only in
	// the worker. Neither flock nor fsync can stall this authority commit.
	base.PersistenceDegraded = true
	persistence.submit(next, generation)
	c.authorityEpoch++
	c.installExpiryWatcherLocked(parent, generation, c.authorityEpoch, next.Exp)
	c.opts.Coordinator.Update(*base)
	// Only events already selected to cause this attempt are consumed. A relay
	// denial arriving after refresh acceptance remains queued for follow-up.
	if c.relayEventSequence > attemptRelaySequence {
		c.relaySignalQueued = true
		c.relayFollowupQueued = true
		c.enqueue(entitlementTrigger{reason: triggerRelayRequired, generation: generation})
	} else {
		c.relaySignalQueued = false
	}
	return true
}

func (c *EntitlementController) applyPersistenceResult(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration uint64, persistence *entitlementPersistenceWorker, result entitlementPersistenceResult) {
	// Version validation and publication share the same critical section as
	// TriggerLicenseChanged, so an obsolete completion cannot clear a newer
	// warning or republish superseded authority.
	c.mu.Lock()
	defer c.mu.Unlock()
	if result.request.version > persistence.completedVersion {
		persistence.completedVersion = result.request.version
	}
	if result.request.version != persistence.latest.version || result.request.generation != generation || generation != c.credentialGeneration {
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
	// A relay-accepted authority newer than the durable marker supersedes that
	// version floor at restart once this cache write succeeds. The marker itself
	// is never cleared and may remain for the lifetime of the credential.
	now := c.opts.Clock.Now()
	if authorityGeneration != generation || !current.ValidAt(relay.SessionSID(base.SessionID), now) || current.CredentialFingerprint != result.request.cached.CredentialFingerprint {
		return
	}
	base.PersistenceDegraded = false
	c.opts.Coordinator.Update(*base)
}

func (c *EntitlementController) applyRevocationResult(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration uint64, persistence *entitlementRevocationWorker, result entitlementRevocationResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if result.request.version > persistence.completedVersion {
		persistence.completedVersion = result.request.version
	}
	if result.request.version != persistence.latest.version || result.request.generation != generation || generation != c.credentialGeneration {
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
	now := c.opts.Clock.Now()
	if authorityGeneration == generation && current.ValidAt(relay.SessionSID(base.SessionID), now) {
		// An accepted recovery may already be waiting on cache durability. The
		// older marker-save completion must not clear that newer warning.
		return
	}
	base.PersistenceDegraded = false
	c.opts.Coordinator.Update(*base)
}

func (c *EntitlementController) revokeAuthorityLocked(now time.Time) {
	c.authorityEpoch++
	if c.expiryCancel != nil {
		c.expiryCancel()
		c.expiryCancel = nil
	}
	next := c.opts.Coordinator.Current()
	publishUnavailable(c.opts.Coordinator, &next, now)
}

func (c *EntitlementController) installExpiryWatcherLocked(_ context.Context, generation, epoch uint64, exp int64) {
	if c.expiryCancel != nil {
		c.expiryCancel()
	}
	watchCtx, cancel := context.WithCancel(c.runCtx)
	c.expiryCancel = cancel
	delay := time.Unix(exp, 0).Sub(c.opts.Clock.Now())
	if delay < 0 {
		delay = 0
	}
	c.expiryWatchers.Add(1)
	go func() {
		defer c.expiryWatchers.Done()
		timer := c.opts.ExpiryClock.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-watchCtx.Done():
			return
		case <-timer.C():
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if watchCtx.Err() != nil || generation != c.credentialGeneration || epoch != c.authorityEpoch {
			return
		}
		now := c.opts.Clock.Now()
		if now.Before(time.Unix(exp, 0)) {
			return
		}
		c.authorityEpoch++
		c.expiryCancel = nil
		next := c.opts.Coordinator.Current()
		publishUnavailable(c.opts.Coordinator, &next, now)
	}()
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
	base.ExpiresAt = time.Unix(cached.Exp, 0)
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

func setTerminal(base *ResolvedRelay, state RelayReadiness) {
	clearRelayStatus(base)
	base.Readiness = state
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

func setNoSeat(base *ResolvedRelay, activations []relay.ActivationSummary) {
	clearRelayStatus(base)
	base.Readiness = RelayReadinessNoSeat
	base.ActivationCount = len(activations)
	if base.ActivationCount > len(base.ActivationSummaries) {
		base.ActivationCount = len(base.ActivationSummaries)
	}
	for i := 0; i < base.ActivationCount; i++ {
		base.ActivationSummaries[i] = RelayActivationSummary{Label: activations[i].Label, FirstSeen: activations[i].FirstSeen}
	}
}
