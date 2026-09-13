package config

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
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

type entitlementRevocationCandidateCache interface {
	LoadRevocationCandidate(string) (relay.CachedEntitlement, bool, error)
}

type entitlementRevocations interface {
	Load() (relay.EntitlementRevocation, bool, error)
	CommitIfUnrevoked(context.Context, string, string, func() bool) (relay.EntitlementRevocation, bool, error)
	SaveContext(context.Context, relay.EntitlementRevocation) error
}

type noopEntitlementRevocations struct{}

func (noopEntitlementRevocations) Load() (relay.EntitlementRevocation, bool, error) {
	return relay.EntitlementRevocation{}, false, nil
}
func (noopEntitlementRevocations) CommitIfUnrevoked(_ context.Context, _, _ string, commit func() bool) (relay.EntitlementRevocation, bool, error) {
	return relay.EntitlementRevocation{}, commit(), nil
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

func (w *entitlementPersistenceWorker) shutdown(parent context.Context, timeout time.Duration) {
	// Authority caching is best effort. Cancel it before the independent terminal
	// marker barrier; only bound the wait for an uncancellable filesystem syscall.
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	w.cancel()
	select {
	case <-w.done:
	case <-drainCtx.Done():
	}
}

type entitlementRevocationRequest struct {
	marker  relay.EntitlementRevocation
	version uint64
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

	stateMu          sync.Mutex
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
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.submitLocked(request)
}

func (w *entitlementRevocationWorker) submitLocked(request entitlementRevocationRequest) {
	if w.latest.version != 0 {
		request.marker = mergeEntitlementRevocations(w.latest.marker, request.marker)
	}
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

func (w *entitlementRevocationWorker) save(marker relay.EntitlementRevocation) {
	w.submit(entitlementRevocationRequest{marker: marker})
}

func (w *entitlementRevocationWorker) retry() {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if !w.needsRetry {
		return
	}
	w.submitLocked(w.latest)
}

func mergeEntitlementRevocations(left, right relay.EntitlementRevocation) relay.EntitlementRevocation {
	merged := relay.EntitlementRevocation{
		SchemaVersion: relay.EntitlementRevocationSchemaVersion,
		Revocations:   make(map[string][]string, len(left.Revocations)+len(right.Revocations)),
	}
	for fingerprint, hashes := range left.Revocations {
		merged.Revocations[fingerprint] = append([]string(nil), hashes...)
	}
	for fingerprint, hashes := range right.Revocations {
		for _, hash := range hashes {
			values := merged.Revocations[fingerprint]
			index := sort.SearchStrings(values, hash)
			if index < len(values) && values[index] == hash {
				continue
			}
			values = append(values, "")
			copy(values[index+1:], values[index:])
			values[index] = hash
			merged.Revocations[fingerprint] = values
		}
	}
	return merged
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
	w.stateMu.Lock()
	if w.latest.version == 0 {
		w.stateMu.Unlock()
		stopAndWait()
		return nil
	}
	targetVersion := w.latest.version
	if w.completedVersion >= targetVersion && !w.needsRetry {
		w.stateMu.Unlock()
		stopAndWait()
		return nil
	}
	if w.needsRetry {
		w.submitLocked(w.latest)
		targetVersion = w.latest.version
	}
	w.stateMu.Unlock()
	for {
		select {
		case result := <-w.results:
			w.stateMu.Lock()
			if result.request.version > w.completedVersion {
				w.completedVersion = result.request.version
			}
			if result.request.version != targetVersion {
				w.stateMu.Unlock()
				continue
			}
			if result.err != nil {
				w.needsRetry = true
				w.submitLocked(w.latest)
				targetVersion = w.latest.version
				w.stateMu.Unlock()
				continue
			}
			w.needsRetry = false
			w.stateMu.Unlock()
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
	closing   bool
	runCtx    context.Context

	credentialGeneration  uint64
	attemptCancel         context.CancelFunc
	relayEventSequence    uint64
	relayAttemptSequence  uint64
	relaySignalQueued     bool
	relayFollowupQueued   bool
	authorityEpoch        uint64
	expiryCancel          context.CancelFunc
	expiryWatchers        sync.WaitGroup
	knownAuthorityHashes  map[uint64]map[string]struct{}
	generationFingerprint map[uint64]string
	revocationPersistence *entitlementRevocationWorker
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
	return &EntitlementController{
		opts: opts, triggers: make(chan entitlementTrigger, 1),
		knownAuthorityHashes: make(map[uint64]map[string]struct{}), generationFingerprint: make(map[uint64]string),
	}
}

// TriggerLicenseChanged supersedes work and runtime authority for the previous
// credential generation. It returns false without mutation after shutdown has
// established its closing boundary. Credential replacement and its validation
// remain the responsibility of the later management API, not this controller.
func (c *EntitlementController) TriggerLicenseChanged() bool {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return false
	}
	oldGeneration := c.credentialGeneration
	// Collect and enqueue every old-generation authority before advancing the
	// generation or permitting attempt cancellation to return. Ledger requests
	// are credential-independent merges, so shutdown cannot discard this work as
	// obsolete merely because the replacement generation is current.
	if marker, ok := c.markerForGenerationLocked(oldGeneration); ok && c.revocationPersistence != nil {
		c.revocationPersistence.save(marker)
	}
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
	return true
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
	if c.closing || generation != c.credentialGeneration {
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

func (c *EntitlementController) rememberAuthorityLocked(generation uint64, fingerprint string, token relay.Secret) {
	if fingerprint == "" || token.Value() == "" {
		return
	}
	if c.knownAuthorityHashes[generation] == nil {
		c.knownAuthorityHashes[generation] = make(map[string]struct{})
	}
	c.knownAuthorityHashes[generation][relay.EntitlementTokenHash(token)] = struct{}{}
	c.generationFingerprint[generation] = fingerprint
}

func (c *EntitlementController) markerForGenerationLocked(generation uint64) (relay.EntitlementRevocation, bool) {
	hashes := c.knownAuthorityHashes[generation]
	fingerprint := c.generationFingerprint[generation]
	if fingerprint == "" || len(hashes) == 0 {
		return relay.EntitlementRevocation{}, false
	}
	values := make([]string, 0, len(hashes))
	for hash := range hashes {
		values = append(values, hash)
	}
	sort.Strings(values)
	return relay.NewEntitlementRevocation(fingerprint, values...), true
}

func (c *EntitlementController) Run(ctx context.Context) {
	c.mu.Lock()
	if c.running || c.closing {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.runCtx = ctx
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.closing = true
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
		// A revocation-only candidate is allowed to bypass wall-clock publication
		// checks, but never the cache's schema, file-security, or credential binding.
		// Remember it solely so a later terminal decision can revoke a durable
		// future-dated authority even after normal Load rejected it.
		candidate := current
		if candidateStore, ok := c.opts.Cache.(entitlementRevocationCandidateCache); ok {
			if loaded, exists, err := candidateStore.LoadRevocationCandidate(credentialFingerprint); err == nil && exists {
				candidate = loaded
			}
		}
		c.mu.Lock()
		c.rememberAuthorityLocked(startupGeneration, credentialFingerprint, candidate.Token)
		c.mu.Unlock()
		// Load the independent marker after potentially blocking cache I/O, so a
		// terminal marker written while Load was blocked wins before publication.
		marker, markerExists, markerErr := c.opts.Revocations.Load()
		if markerErr != nil || (markerExists && marker.Revokes(credentialFingerprint, current.Token)) {
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
	c.mu.Lock()
	c.revocationPersistence = revocationPersistence
	c.mu.Unlock()
	defer func() {
		// Establish the closing boundary while revocation submissions still point
		// at this worker. TriggerLicenseChanged cannot advance the credential
		// generation after the barrier takes its final request snapshot.
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		// Cancel old authority I/O before entering the independent marker barrier.
		// Both workers share one shutdown budget, but blocked cache I/O cannot
		// consume time before the marker gets its durability opportunity.
		persistence.cancel()
		started := time.Now()
		if result := revocationPersistence.shutdown(ctx, c.opts.PersistenceDrainTimeout); result != nil {
			c.applyRevocationResult(c.generation(), &base, &current, authorityGeneration, revocationPersistence, *result)
		}
		remaining := c.opts.PersistenceDrainTimeout - time.Since(started)
		if remaining < 0 {
			remaining = 0
		}
		persistence.shutdown(ctx, remaining)
		c.mu.Lock()
		if c.revocationPersistence == revocationPersistence {
			c.revocationPersistence = nil
		}
		c.mu.Unlock()
	}()
	for {
		generation := c.generation()
		if authorityGeneration != generation {
			c.mu.Lock()
			marker, revokeOldGeneration := c.markerForGenerationLocked(authorityGeneration)
			c.mu.Unlock()
			if revokeOldGeneration {
				persistence.discard()
				revocationPersistence.save(marker)
			}
			current = relay.CachedEntitlement{}
			valid = false
			base = c.opts.Coordinator.Current()
			base.PersistenceDegraded = revokeOldGeneration
			if revokeOldGeneration {
				c.opts.Coordinator.Update(base)
			}
		}
		persistence.retry(generation)
		revocationPersistence.retry()
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
		if c.publishTerminalForGeneration(generation, base, current, credentialFingerprint, authorityGeneration, persistence, revocationPersistence, RelayReadinessNeedsLicense, nil) {
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
				if c.publishTerminalForGeneration(generation, base, current, credentialFingerprint, authorityGeneration, persistence, revocationPersistence, state, issuerErr.Activations) {
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
	// Record the validated candidate before relay I/O. A refresh may reach relay
	// acceptance even when credential replacement concurrently cancels the local
	// request, so replacement must already be able to enqueue its exact hash.
	c.mu.Lock()
	if generation == c.credentialGeneration {
		c.rememberAuthorityLocked(generation, next.CredentialFingerprint, next.Token)
	}
	c.mu.Unlock()
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
	// The durable ledger check and the short in-memory authority commit share
	// both ledger locks. A concurrent append therefore wins before this callback
	// or follows one controller-owned commit whose later event revokes it.
	marker, committed, markerErr := c.opts.Revocations.CommitIfUnrevoked(ctx, next.CredentialFingerprint, relay.EntitlementTokenHash(next.Token), func() bool {
		return c.commitIssued(ctx, generation, attemptRelaySequence, base, current, authorityGeneration, persistence, next, issued.Entitlement)
	})
	if markerErr != nil {
		c.publishRejectedIssuedForGeneration(generation, base, current, authorityGeneration, marker, next, true)
		return 0, false
	}
	if marker.Revokes(next.CredentialFingerprint, next.Token) {
		c.publishRejectedIssuedForGeneration(generation, base, current, authorityGeneration, marker, next, false)
		return 0, false
	}
	if !committed {
		return 0, false
	}
	return base.RenewsAt.Sub(c.opts.Clock.Now()), false
}

func (c *EntitlementController) publishRejectedIssuedForGeneration(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, marker relay.EntitlementRevocation, issued relay.CachedEntitlement, forceUnavailable bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration || c.closing {
		return false
	}
	currentFingerprint := current.CredentialFingerprint
	if currentFingerprint == "" {
		currentFingerprint = issued.CredentialFingerprint
	}
	currentRevoked := current.Token.Value() != "" && (relay.EntitlementTokenHash(current.Token) == relay.EntitlementTokenHash(issued.Token) || marker.Revokes(currentFingerprint, current.Token))
	if forceUnavailable || currentRevoked {
		*current = relay.CachedEntitlement{}
		*authorityGeneration = generation
		c.authorityEpoch++
		if c.expiryCancel != nil {
			c.expiryCancel()
			c.expiryCancel = nil
		}
		publishUnavailable(c.opts.Coordinator, base, c.opts.Clock.Now())
		return true
	}
	if *authorityGeneration == generation && current.ValidAt(relay.SessionSID(base.SessionID), c.opts.Clock.Now()) {
		publishPending(c.opts.Coordinator, base, *current)
	} else {
		publishUnavailable(c.opts.Coordinator, base, c.opts.Clock.Now())
	}
	return true
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

func (c *EntitlementController) publishTerminalForGeneration(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, credentialFingerprint *string, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, revocationPersistence *entitlementRevocationWorker, state RelayReadiness, activations []relay.ActivationSummary) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration {
		return false
	}
	c.rememberAuthorityLocked(generation, *credentialFingerprint, current.Token)
	if persistence.latest.generation == generation {
		c.rememberAuthorityLocked(generation, persistence.latest.cached.CredentialFingerprint, persistence.latest.cached.Token)
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
		marker, hasAuthority := c.markerForGenerationLocked(generation)
		if hasAuthority {
			base.PersistenceDegraded = true
			revocationPersistence.save(marker)
		}
	}
	c.opts.Coordinator.Update(*base)
	return true
}

func (c *EntitlementController) commitIssued(parent context.Context, generation, attemptRelaySequence uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration *uint64, persistence *entitlementPersistenceWorker, next relay.CachedEntitlement, issued relay.Entitlement) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if parent.Err() != nil || c.closing || generation != c.credentialGeneration {
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
	c.rememberAuthorityLocked(generation, next.CredentialFingerprint, next.Token)
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
	// A relay-accepted token with a distinct hash remains eligible even while an
	// older exact-token revocation marker persists for this credential.
	now := c.opts.Clock.Now()
	if authorityGeneration != generation || !current.ValidAt(relay.SessionSID(base.SessionID), now) || current.CredentialFingerprint != result.request.cached.CredentialFingerprint {
		return
	}
	base.PersistenceDegraded = false
	c.opts.Coordinator.Update(*base)
}

func (c *EntitlementController) applyRevocationResult(generation uint64, base *ResolvedRelay, current *relay.CachedEntitlement, authorityGeneration uint64, persistence *entitlementRevocationWorker, result entitlementRevocationResult) {
	persistence.stateMu.Lock()
	if result.request.version > persistence.completedVersion {
		persistence.completedVersion = result.request.version
	}
	latest := result.request.version == persistence.latest.version
	if latest {
		persistence.needsRetry = result.err != nil
	}
	persistence.stateMu.Unlock()
	if !latest || result.err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.credentialGeneration || !base.PersistenceDegraded {
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
