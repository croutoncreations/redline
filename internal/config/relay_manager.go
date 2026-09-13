package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jfox/redline/internal/relay"
)

// RelayConnectionState is a presentation of proven dialer state. "connected"
// is emitted only while the host WebSocket handshake is live.
type RelayConnectionState string

const (
	RelayDisconnected RelayConnectionState = "disconnected"
	RelayConnecting   RelayConnectionState = "connecting"
	RelayConnected    RelayConnectionState = "connected"
)

// RelayStatus is the closed, non-secret management view. In particular it does
// not expose the session id, issuer URL, license key, or entitlement token.
type RelayStatus struct {
	State       RelayReadiness           `json:"state"`
	Mode        RelayMode                `json:"mode"`
	URL         string                   `json:"url,omitempty"`
	Connection  RelayConnectionState     `json:"connection"`
	RenewsAt    *time.Time               `json:"renews_at,omitempty"`
	ExpiresAt   *time.Time               `json:"expires_at,omitempty"`
	Since       *time.Time               `json:"since,omitempty"`
	Seats       int                      `json:"seats,omitempty"`
	SeatsUsed   int                      `json:"seats_used,omitempty"`
	MaxClients  int                      `json:"max_clients,omitempty"`
	Activations []RelayActivationSummary `json:"activations,omitempty"`
}

// MarshalJSON is the single public relay-status encoder. It enforces the
// state-specific required fields and never emits fields from another variant.
func (s RelayStatus) MarshalJSON() ([]byte, error) {
	if s.State == RelayReadinessHostedConfigured {
		return nil, errors.New("hosted_configured is not a public relay status")
	}
	output := map[string]any{
		"state": s.State, "mode": s.Mode, "connection": s.Connection,
	}
	if s.URL != "" {
		output["url"] = s.URL
	}
	if s.Seats != 0 {
		output["seats"] = s.Seats
	}
	if s.SeatsUsed != 0 {
		output["seats_used"] = s.SeatsUsed
	}
	if s.MaxClients != 0 {
		output["max_clients"] = s.MaxClients
	}
	switch s.State {
	case RelayReadinessActive:
		if s.RenewsAt == nil {
			return nil, errors.New("active relay status requires renews_at")
		}
		output["renews_at"] = s.RenewsAt
	case RelayReadinessRenewPending:
		if s.ExpiresAt == nil {
			return nil, errors.New("renew_pending relay status requires expires_at")
		}
		output["expires_at"] = s.ExpiresAt
	case RelayReadinessUnavailable:
		if s.Since == nil {
			return nil, errors.New("unavailable relay status requires since")
		}
		output["since"] = s.Since
	case RelayReadinessNoSeat:
		activations := s.Activations
		if activations == nil {
			activations = []RelayActivationSummary{}
		}
		output["activations"] = activations
	}
	return json.Marshal(output)
}

// RelayConfigureRequest is accepted by the authenticated local API.
type RelayConfigureRequest struct {
	Mode       RelayMode `json:"mode"`
	URL        string    `json:"url,omitempty"`
	LicenseKey string    `json:"license_key,omitempty"`
	Label      string    `json:"label,omitempty"`
}

// RelayManagementError has a stable, non-secret code suitable for API and CLI
// consumers. Its message never includes request credentials or issuer bodies.
type RelayManagementError struct {
	Code string
	Err  error
}

func (e *RelayManagementError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}
func (e *RelayManagementError) Unwrap() error { return e.Err }

// RelayManagementIssuer is the issuer surface owned by RelayManager.
type RelayManagementIssuer interface {
	Entitlement(context.Context, string, string, string) (relay.ReceivedEntitlement, error)
	Activations(context.Context, string) ([]relay.Activation, error)
	DeleteActivation(context.Context, string, string) error
	Portal(context.Context, string) (*url.URL, error)
}

type RelayManagerOptions struct {
	State          *RelayStateStore
	Licenses       LicenseStore
	Issuer         RelayManagementIssuer
	IssuerFactory  RelayIssuerFactory
	Controller     *EntitlementController
	Coordinator    *RelayCoordinator
	AttemptTimeout time.Duration
	Now            func() time.Time
}

type relayManagerLifecycle uint8

const (
	relayManagerOpen relayManagerLifecycle = iota
	relayManagerClosing
	relayManagerClosed
)

// RelayManager is the service's sole relay management authority. API handlers
// and CLI commands can ask it for operations but cannot reach Keychain, issuer,
// controller, state files, or runtime mutation independently.
type RelayManager struct {
	state                *RelayStateStore
	licenses             LicenseStore
	issuer               RelayManagementIssuer
	issuerFactory        RelayIssuerFactory
	controller           *EntitlementController
	coordinator          *RelayCoordinator
	timeout              time.Duration
	now                  func() time.Time
	mutations            sync.Mutex
	lifecycleMu          sync.Mutex
	lifecycle            relayManagerLifecycle
	operations           sync.WaitGroup
	connectionMu         sync.Mutex
	connectionGeneration uint64
}

// NewServiceRelayManager constructs the complete production relay vertical
// slice. Service bootstrap receives one owner rather than separate state,
// Keychain, issuer, controller, and coordinator capabilities.
func NewServiceRelayManager(ctx context.Context, cfg Config) (*RelayManager, error) {
	licenses := DefaultLicenseStore()
	state := NewRelayStateStore(DefaultRelayStatePath(cfg.Relay.KeypairPath, cfg.Database))
	resolved, err := NewRelayResolver(state, licenses).Resolve(ctx, cfg.Relay)
	if err != nil {
		return nil, fmt.Errorf("resolve relay: %w", err)
	}
	if err := ValidateEffectiveRelay(cfg, resolved); err != nil {
		return nil, err
	}
	issuerFactory := RelayIssuerFactory(func(issuerURL string) (RelayManagementIssuer, error) {
		if issuerURL == "" {
			issuerURL = DefaultIssuerURL
		}
		return relay.NewIssuerClient(issuerURL, nil)
	})
	issuer, err := issuerFactory(resolved.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("relay issuer: %w", err)
	}
	coordinator := NewRelayCoordinator(resolved)
	identityPath := relay.DefaultKeypairPath(cfg.Relay.KeypairPath, cfg.Database)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator:   coordinator,
		Initial:       resolved,
		Licenses:      licenses,
		Issuer:        issuer,
		IssuerFactory: issuerFactory,
		Cache:         relay.NewEntitlementCacheStore(relay.DefaultEntitlementCachePath(identityPath)),
		Revocations:   relay.NewEntitlementRevocationStore(relay.DefaultEntitlementRevocationPath(relay.DefaultEntitlementCachePath(identityPath))),
	})
	return NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, Issuer: issuer, IssuerFactory: issuerFactory,
		Controller: controller, Coordinator: coordinator,
	}), nil
}

func NewRelayManager(opts RelayManagerOptions) *RelayManager {
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = 16 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &RelayManager{
		state: opts.State, licenses: opts.Licenses, issuer: opts.Issuer, issuerFactory: opts.IssuerFactory,
		controller: opts.Controller, coordinator: opts.Coordinator,
		timeout: opts.AttemptTimeout, now: opts.Now,
	}
}

func (m *RelayManager) Current() ResolvedRelay { return m.coordinator.Current() }
func (m *RelayManager) Subscribe() (<-chan ResolvedRelay, func()) {
	return m.coordinator.Subscribe()
}
func (m *RelayManager) Run(ctx context.Context) {
	// A pending deactivation is retried before the controller is allowed to load
	// credentials or contact an issuer for entitlement renewal. Gate the
	// controller unconditionally from the in-memory snapshot alone, before any
	// fallible disk/Keychain/issuer work below: a transient failure inside
	// Deactivate must never leave the controller free to renew and re-register
	// while the durable intent remains unresolved. PrepareGeneration cannot
	// itself fail on I/O; it only revokes authority and bumps the generation,
	// so the controller stays gated (uncommitted) even if Deactivate's own
	// internal retry never reaches its matching commit.
	// This call and Deactivate's own internal prepareController call are two
	// independent PrepareGeneration invocations by design, not a bug: this one
	// exists solely to gate the controller synchronously before any fallible
	// work runs, so it must not be skipped even if Deactivate's own call is
	// never reached (e.g. it fails before calling prepareController itself).
	// The token returned here is deliberately not committed or aborted; its
	// only purpose was already served the instant it revoked authority.
	if m.coordinator.Current().Deactivation != nil {
		if m.controller != nil {
			m.controller.PrepareGeneration(ctx)
		}
		_, _ = m.Deactivate(ctx)
	}
	if m.controller != nil {
		m.controller.Run(ctx)
		return
	}
	<-ctx.Done()
}

func (m *RelayManager) beginOperation() error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.lifecycle != relayManagerOpen {
		return &RelayManagementError{Code: "management_closing", Err: errors.New("relay management is closing")}
	}
	m.operations.Add(1)
	return nil
}

func (m *RelayManager) endOperation() { m.operations.Done() }

// CloseAdmission rejects mutations before persistence and waits for operations
// already admitted while the entitlement controller remains alive.
func (m *RelayManager) CloseAdmission(ctx context.Context) error {
	m.lifecycleMu.Lock()
	if m.lifecycle == relayManagerClosed {
		m.lifecycleMu.Unlock()
		return nil
	}
	m.lifecycle = relayManagerClosing
	m.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { m.operations.Wait(); close(done) }()
	select {
	case <-done:
		m.lifecycleMu.Lock()
		m.lifecycle = relayManagerClosed
		m.lifecycleMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetConnected is called only by the dialer lifecycle. Atomic modification
// prevents a disconnect racing a renewal from restoring stale authority.
func (m *RelayManager) TriggerRelayEntitlement(signal relay.EntitlementSignal) {
	if m.controller != nil {
		m.controller.TriggerRelayEntitlement(signal)
	}
}

type RelayConnectionIdentity struct {
	URL                  string
	SessionID            string
	ReconnectGeneration  uint64
	ConnectionGeneration uint64
}

// SetConnection accepts only the newest matching dialer lifecycle. Delayed
// callbacks from a canceled socket cannot change connection truth.
func (m *RelayManager) SetConnection(identity RelayConnectionIdentity, connected bool) {
	m.connectionMu.Lock()
	defer m.connectionMu.Unlock()
	m.coordinator.Modify(func(current ResolvedRelay) ResolvedRelay {
		if identity.URL != current.URL || identity.SessionID != current.SessionID || identity.ReconnectGeneration != current.ReconnectGeneration || identity.ConnectionGeneration < m.connectionGeneration {
			return current
		}
		m.connectionGeneration = identity.ConnectionGeneration
		current.Connected = connected && current.CanDial()
		return current
	})
}

// SetConnected remains for embedders that do not yet supply an identity.
func (m *RelayManager) SetConnected(connected bool) {
	current := m.coordinator.Current()
	m.connectionMu.Lock()
	generation := m.connectionGeneration
	m.connectionMu.Unlock()
	m.SetConnection(RelayConnectionIdentity{URL: current.URL, SessionID: current.SessionID, ReconnectGeneration: current.ReconnectGeneration, ConnectionGeneration: generation}, connected)
}

func (m *RelayManager) Status() RelayStatus { return relayStatus(m.coordinator.Current(), m.now) }

// NewRelayStatus constructs the closed public view from one immutable runtime
// snapshot, mapping internal staging values fail-closed. It never samples wall
// time for a coherent snapshot; time.Now is used only as the explicit,
// injectable fallback for the rare malformed/internal-staging case, so the
// same coherent snapshot always renders identically across repeated calls.
func NewRelayStatus(current ResolvedRelay) RelayStatus { return relayStatus(current, time.Now) }

func relayStatus(current ResolvedRelay, now func() time.Time) RelayStatus {
	state := current.Readiness
	switch state {
	case RelayReadinessOff, RelayReadinessSelfHosted, RelayReadinessNeedsLicense, RelayReadinessActive, RelayReadinessRenewPending, RelayReadinessUnavailable, RelayReadinessLapsed, RelayReadinessNoSeat, RelayReadinessInvalidKey:
	default:
		state = RelayReadinessUnavailable
	}
	if current.Readiness == RelayReadinessHostedConfigured || (state == RelayReadinessActive && current.RenewsAt.IsZero()) || (state == RelayReadinessRenewPending && current.ExpiresAt.IsZero()) {
		// Internal or malformed controller staging values never cross the public
		// status boundary as an impossible variant.
		state = RelayReadinessUnavailable
		if current.UnavailableSince.IsZero() {
			current.UnavailableSince = now().UTC()
		}
	}
	status := RelayStatus{
		State: state, Mode: current.Mode, URL: current.URL,
		Seats: current.Seats, SeatsUsed: current.SeatsUsed, MaxClients: current.MaxClients,
	}
	if current.CanDial() {
		status.Connection = RelayConnecting
		if current.Connected {
			status.Connection = RelayConnected
		}
	} else {
		status.Connection = RelayDisconnected
	}
	switch state {
	case RelayReadinessActive:
		value := current.RenewsAt
		status.RenewsAt = &value
	case RelayReadinessRenewPending:
		value := current.ExpiresAt
		status.ExpiresAt = &value
	case RelayReadinessUnavailable:
		if current.UnavailableSince.IsZero() {
			current.UnavailableSince = now().UTC()
		}
		value := current.UnavailableSince
		status.Since = &value
	case RelayReadinessNoSeat:
		status.Activations = current.Activations()
	}
	return status
}

// ValidateRelayConfiguration is pure: malformed requests fail before state,
// Keychain, issuer, identity, or controller work occurs.
func ValidateRelayConfiguration(request RelayConfigureRequest) (RelayConfigureRequest, error) {
	request.URL = strings.TrimSpace(request.URL)
	request.Label = strings.TrimSpace(request.Label)
	if len(request.Label) > 200 || strings.IndexFunc(request.Label, unicode.IsControl) >= 0 {
		return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("relay label must be at most 200 characters and contain no control characters")}
	}
	if len(request.LicenseKey) > 4096 || strings.TrimSpace(request.LicenseKey) != request.LicenseKey || strings.IndexFunc(request.LicenseKey, unicode.IsControl) >= 0 {
		return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("license_key is malformed")}
	}
	switch request.Mode {
	case RelayModeHosted:
		if request.URL != "" {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("hosted relay configuration does not accept url")}
		}
	case RelayModeSelfHosted:
		if request.URL == "" {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("self_hosted relay configuration requires url")}
		}
		if request.URL == DefaultHostedRelayURL {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("self_hosted relay configuration requires a custom url")}
		}
		if err := validateSafeEndpoint(request.URL); err != nil {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: fmt.Errorf("relay url: %w", err)}
		}
		if request.LicenseKey != "" {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("self_hosted relay configuration does not accept license_key")}
		}
	case RelayModeOff:
		if request.URL != "" || request.LicenseKey != "" || request.Label != "" {
			return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("off relay configuration does not accept url, license_key, or label")}
		}
	default:
		return RelayConfigureRequest{}, &RelayManagementError{Code: "invalid_request", Err: errors.New("mode must be hosted, self_hosted, or off")}
	}
	return request, nil
}

func (m *RelayManager) Configure(ctx context.Context, raw RelayConfigureRequest) (RelayStatus, error) {
	request, err := ValidateRelayConfiguration(raw)
	if err != nil {
		return RelayStatus{}, err
	}
	if err := m.beginOperation(); err != nil {
		return RelayStatus{}, err
	}
	defer m.endOperation()
	m.mutations.Lock()
	defer m.mutations.Unlock()
	return m.configureTransactionLocked(ctx, request, false)
}

func (m *RelayManager) configureTransactionLocked(ctx context.Context, request RelayConfigureRequest, clearLicense bool) (RelayStatus, error) {
	previousState, exists, err := m.state.Load()
	if err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("relay state is unavailable")}
	}
	previousRuntime := m.coordinator.Current()
	if !exists {
		previousState = previousRuntime.RelayManagedState
	}
	if previousState.Deactivation != nil {
		return RelayStatus{}, &RelayManagementError{Code: "deactivation_pending", Err: errors.New("relay deactivation must complete before reconfiguration")}
	}
	var previousLicense string
	var licenseErr error
	hadLicense := false
	if request.Mode == RelayModeHosted || clearLicense {
		previousLicense, licenseErr = m.licenses.Load(ctx)
		if licenseErr != nil && !errors.Is(licenseErr, ErrLicenseNotFound) {
			return RelayStatus{}, &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("relay secure store is unavailable")}
		}
		hadLicense = licenseErr == nil && previousLicense != ""
	}

	nextState := RelayManagedState{Mode: request.Mode, Label: request.Label}
	if request.Mode != RelayModeOff {
		nextState.SessionID = previousState.SessionID
		if nextState.SessionID == "" {
			nextState.SessionID, err = relay.NewSessionID()
			if err != nil {
				return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not create relay session")}
			}
		}
	}
	if request.Mode == RelayModeHosted {
		nextState.URL = DefaultHostedRelayURL
		nextState.IssuerURL = DefaultIssuerURL
		if request.LicenseKey == "" && previousState.Mode == RelayModeHosted && previousState.IssuerURL != "" {
			// Reusing an existing credential must never silently move it to a
			// different issuer. Preserve the issuer the credential is already
			// bound to; only an explicit new key may change it.
			nextState.IssuerURL = previousState.IssuerURL
		}
	} else if request.Mode == RelayModeSelfHosted {
		nextState.URL = request.URL
	}
	if request.Mode == RelayModeHosted && request.LicenseKey == "" && hadLicense && previousState.Mode != RelayModeHosted {
		// The existing Keychain credential was never proven to belong to this
		// issuer generation (managed state was not already hosted), so it must
		// not be reused across an issuer boundary without an explicit key.
		return RelayStatus{}, &RelayManagementError{Code: "license_key_required", Err: errors.New("a license_key is required to enable hosted relay")}
	}
	licenseChanged := request.LicenseKey != "" && (!hadLicense || request.LicenseKey != previousLicense)
	changedAuthority := previousState.Mode != nextState.Mode || previousState.URL != nextState.URL || previousState.IssuerURL != nextState.IssuerURL || previousState.SessionID != nextState.SessionID || licenseChanged || clearLicense || previousState.Deactivation != nil
	if changedAuthority {
		nextState.Generation = previousState.Generation + 1
	} else {
		nextState.Generation = previousState.Generation
	}
	if err := validateRelayManagedState(nextState); err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "invalid_request", Err: err}
	}

	var prepared RelayControllerPreparation
	if changedAuthority {
		if m.controller != nil {
			var ok bool
			prepared, ok = m.controller.PrepareGeneration(ctx)
			if !ok {
				return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
			}
		} else {
			m.publishFailClosed(previousRuntime)
		}
	}

	licenseMutated := false
	if request.Mode == RelayModeHosted && request.LicenseKey != "" {
		err = m.licenses.Replace(ctx, request.LicenseKey)
		licenseMutated = err == nil
	} else if clearLicense {
		err = m.licenses.Clear(ctx)
		licenseMutated = err == nil
	}
	if err != nil {
		m.abortPrepared(prepared, previousRuntime)
		return RelayStatus{}, &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("relay secure store update failed")}
	}

	durable := false
	// safeToRestorePrevious is true only when we have positive proof (a
	// successful reload matching the exact previous state) that disk still
	// holds the old value. Restoring the previous Keychain credential in any
	// other ambiguous case could pair it with a disk state that actually holds
	// the new issuer, disclosing the old credential to the wrong issuer.
	safeToRestorePrevious := false
	err = m.state.Save(nextState)
	if err == nil {
		durable = true
	} else {
		var uncertain *RelayStateCommitError
		if errors.As(err, &uncertain) {
			observed, observedExists, loadErr := m.state.Load()
			switch {
			case loadErr == nil && observedExists && relayManagedStatesEqual(observed, nextState):
				durable = true
			case loadErr == nil && observedExists && relayManagedStatesEqual(observed, previousState):
				safeToRestorePrevious = true
			case loadErr == nil && !observedExists && !exists:
				safeToRestorePrevious = true
			}
		} else {
			// Rename never published (a plain, non-commit-uncertain failure): disk
			// provably still holds the previous state.
			safeToRestorePrevious = true
		}
	}
	if !durable {
		var compensation error
		if licenseMutated {
			if safeToRestorePrevious {
				compensation = m.restoreLicense(ctx, previousLicense, hadLicense)
			} else {
				// Disk state after this rename is unproven. Never restore a
				// credential that might now mismatch the issuer actually on disk;
				// fail closed by clearing Keychain authority instead. The next
				// resolve/startup will observe whatever generation truly landed
				// and require an explicit key before any issuer call.
				compensation = m.licenses.Clear(ctx)
			}
		}
		m.abortPrepared(prepared, previousRuntime)
		if compensation != nil {
			return RelayStatus{}, &RelayManagementError{Code: "compensation_failed", Err: errors.New("relay state update failed and secure store compensation failed")}
		}
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("relay state update failed")}
	}

	next := resolvedAfterConfigure(previousRuntime, nextState, request.Mode == RelayModeHosted && (request.LicenseKey != "" || hadLicense), changedAuthority)
	if prepared.attemptID != 0 {
		next.ControllerAttemptID = prepared.attemptID
	}
	m.coordinator.Update(next)
	if m.controller == nil || !changedAuthority {
		return relayStatus(next, m.now), nil
	}
	updates, unsubscribe := m.coordinator.Subscribe()
	defer unsubscribe()
	if !m.controller.CommitGeneration(prepared) {
		// The managed mutation is already durable. Reconcile it visibly and
		// fail-closed rather than returning a failure that hides persisted state.
		m.publishFailClosed(next)
		return relayStatus(m.coordinator.Current(), m.now), nil
	}
	if next.Mode != RelayModeHosted || next.Readiness == RelayReadinessNeedsLicense {
		return relayStatus(next, m.now), nil
	}
	return m.waitForHostedAttempt(ctx, updates, next.Generation, prepared.attemptID)
}

func resolvedAfterConfigure(previous ResolvedRelay, state RelayManagedState, hasLicense, changedAuthority bool) ResolvedRelay {
	if !changedAuthority {
		previous.RelayManagedState = state
		return previous
	}
	next := ResolvedRelay{RelayManagedState: state}
	switch state.Mode {
	case RelayModeOff:
		next.Readiness = RelayReadinessOff
	case RelayModeSelfHosted:
		next.Readiness, next.Dial = RelayReadinessSelfHosted, true
	case RelayModeHosted:
		if hasLicense {
			next.Readiness = RelayReadinessHostedConfigured
		} else {
			next.Readiness = RelayReadinessNeedsLicense
		}
	}
	return next
}

func (m *RelayManager) publishFailClosed(base ResolvedRelay) {
	clearRelayStatus(&base)
	base.Connected = false
	base.Readiness = RelayReadinessUnavailable
	base.UnavailableSince = m.now().UTC()
	m.coordinator.Update(base)
}

func (m *RelayManager) abortPrepared(prepared RelayControllerPreparation, previous ResolvedRelay) {
	if prepared.generation != 0 && m.controller != nil {
		m.controller.AbortGeneration(prepared)
	}
	m.publishFailClosed(previous)
}

func (m *RelayManager) waitForHostedAttempt(ctx context.Context, updates <-chan ResolvedRelay, stateGeneration, attemptID uint64) (RelayStatus, error) {
	waitCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	for {
		select {
		case current, ok := <-updates:
			if !ok {
				return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
			}
			if current.Generation != stateGeneration || (attemptID != 0 && current.ControllerCompletedAttemptID != attemptID) {
				continue
			}
			if current.Readiness != RelayReadinessHostedConfigured {
				return relayStatus(current, m.now), nil
			}
		case <-waitCtx.Done():
			return RelayStatus{}, &RelayManagementError{Code: "pending", Err: errors.New("relay activation is still pending")}
		}
	}
}

func (m *RelayManager) restoreLicense(ctx context.Context, previous string, existed bool) error {
	if existed {
		return m.licenses.Replace(ctx, previous)
	}
	return m.licenses.Clear(ctx)
}

type relayIssuerGeneration struct {
	state   RelayManagedState
	license string
	issuer  RelayManagementIssuer
}

func (m *RelayManager) issuerGeneration(ctx context.Context) (relayIssuerGeneration, error) {
	state, exists, err := m.state.Load()
	if err != nil || !exists || state.Mode != RelayModeHosted {
		return relayIssuerGeneration{}, &RelayManagementError{Code: "not_hosted", Err: errors.New("hosted relay is not configured")}
	}
	license, err := m.licenses.Load(ctx)
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		return relayIssuerGeneration{}, &RelayManagementError{Code: "needs_license", Err: errors.New("hosted relay license is required")}
	}
	if err != nil {
		return relayIssuerGeneration{}, &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("relay secure store is unavailable")}
	}
	issuer := m.issuer
	if m.issuerFactory != nil {
		issuer, err = m.issuerFactory(state.IssuerURL)
		if err != nil {
			return relayIssuerGeneration{}, &RelayManagementError{Code: "issuer_unavailable", Err: errors.New("relay issuer is unavailable")}
		}
	}
	if issuer == nil {
		return relayIssuerGeneration{}, &RelayManagementError{Code: "issuer_unavailable", Err: errors.New("relay issuer is unavailable")}
	}
	return relayIssuerGeneration{state: state, license: license, issuer: issuer}, nil
}

func (m *RelayManager) Devices(ctx context.Context) ([]relay.Activation, error) {
	if err := m.beginOperation(); err != nil {
		return nil, err
	}
	defer m.endOperation()
	m.mutations.Lock()
	defer m.mutations.Unlock()
	generation, err := m.issuerGeneration(ctx)
	if err != nil {
		return nil, err
	}
	activations, err := generation.issuer.Activations(ctx, generation.license)
	if err != nil {
		return nil, managementIssuerError(err)
	}
	return activations, nil
}

func (m *RelayManager) Portal(ctx context.Context) (*url.URL, error) {
	if err := m.beginOperation(); err != nil {
		return nil, err
	}
	defer m.endOperation()
	m.mutations.Lock()
	defer m.mutations.Unlock()
	generation, err := m.issuerGeneration(ctx)
	if err != nil {
		return nil, err
	}
	portal, err := generation.issuer.Portal(ctx, generation.license)
	if err != nil {
		return nil, managementIssuerError(err)
	}
	return portal, nil
}

func (m *RelayManager) DeactivateDevice(ctx context.Context, id string) error {
	if !validRelayActivationID(id) {
		return &RelayManagementError{Code: "invalid_request", Err: errors.New("activation id is malformed")}
	}
	if err := m.beginOperation(); err != nil {
		return err
	}
	defer m.endOperation()
	m.mutations.Lock()
	defer m.mutations.Unlock()
	generation, err := m.issuerGeneration(ctx)
	if err != nil {
		return err
	}
	activations, err := generation.issuer.Activations(ctx, generation.license)
	if err != nil {
		return managementIssuerError(err)
	}
	var found *relay.Activation
	for index := range activations {
		if activations[index].ID == id {
			found = &activations[index]
			break
		}
	}
	if found == nil {
		return &RelayManagementError{Code: "activation_not_found", Err: errors.New("relay activation was not found")}
	}
	if !found.Current {
		if err := generation.issuer.DeleteActivation(ctx, generation.license, id); err != nil {
			return managementIssuerError(err)
		}
		return nil
	}
	_, err = m.deactivateCurrentLocked(ctx, generation, id)
	return err
}

func (m *RelayManager) Deactivate(ctx context.Context) (RelayStatus, error) {
	if err := m.beginOperation(); err != nil {
		return RelayStatus{}, err
	}
	defer m.endOperation()
	m.mutations.Lock()
	defer m.mutations.Unlock()
	state, exists, stateErr := m.state.Load()
	if stateErr != nil {
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("relay state is unavailable")}
	}
	if exists && state.Deactivation != nil && state.Deactivation.RemoteDeleted {
		return m.deactivateCurrentLocked(ctx, relayIssuerGeneration{state: state}, state.Deactivation.ActivationID)
	}
	generation, err := m.issuerGeneration(ctx)
	if err != nil {
		return RelayStatus{}, err
	}
	if generation.state.Deactivation != nil && !generation.state.Deactivation.DiscoverCurrent {
		return m.deactivateCurrentLocked(ctx, generation, generation.state.Deactivation.ActivationID)
	}
	prepared, ok := m.prepareController(ctx)
	if !ok {
		return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
	}
	if generation.state.Deactivation == nil {
		generation.state.Generation++
		generation.state.Deactivation = &RelayDeactivationIntent{StateGeneration: generation.state.Generation, DiscoverCurrent: true}
		if durable, _ := m.saveObservedState(generation.state); !durable {
			return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not persist relay deactivation intent")}
		}
		m.coordinator.Update(ResolvedRelay{RelayManagedState: generation.state, Readiness: RelayReadinessUnavailable, UnavailableSince: m.now().UTC(), ControllerAttemptID: prepared.attemptID})
	}
	activations, err := generation.issuer.Activations(ctx, generation.license)
	if err != nil {
		return RelayStatus{}, managementIssuerError(err)
	}
	currentID := ""
	for _, activation := range activations {
		if activation.Current {
			if currentID != "" {
				return RelayStatus{}, &RelayManagementError{Code: "activation_conflict", Err: errors.New("issuer returned multiple current activations")}
			}
			currentID = activation.ID
		}
	}
	if currentID == "" {
		// The issuer already has no current activation for this credential: the
		// remote side of deactivation is a no-op. Complete the durable transition
		// directly instead of returning an error that would leave the intent
		// stuck forever (every retry would re-discover the same empty list).
		generation.state.Generation++
		generation.state.Deactivation = &RelayDeactivationIntent{StateGeneration: generation.state.Generation, RemoteDeleted: true}
		if durable, _ := m.saveObservedState(generation.state); !durable {
			return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not persist relay deactivation absence")}
		}
		return m.finishDeactivationLocked(ctx, generation, "", prepared)
	}
	return m.finishDeactivationLocked(ctx, generation, currentID, prepared)
}

func (m *RelayManager) prepareController(ctx context.Context) (RelayControllerPreparation, bool) {
	if m.controller != nil {
		return m.controller.PrepareGeneration(ctx)
	}
	m.publishFailClosed(m.coordinator.Current())
	return RelayControllerPreparation{generation: 1}, true
}

func (m *RelayManager) deactivateCurrentLocked(ctx context.Context, generation relayIssuerGeneration, id string) (RelayStatus, error) {
	prepared, ok := m.prepareController(ctx)
	if !ok {
		return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
	}
	return m.finishDeactivationLocked(ctx, generation, id, prepared)
}

func (m *RelayManager) finishDeactivationLocked(ctx context.Context, generation relayIssuerGeneration, id string, prepared RelayControllerPreparation) (RelayStatus, error) {
	state := generation.state
	if state.Deactivation == nil {
		state.Generation++
		state.Deactivation = &RelayDeactivationIntent{ActivationID: id, StateGeneration: state.Generation}
		if durable, _ := m.saveObservedState(state); !durable {
			return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not persist relay deactivation intent")}
		}
	} else if state.Deactivation.DiscoverCurrent {
		state.Deactivation.ActivationID = id
		state.Deactivation.DiscoverCurrent = false
		if durable, _ := m.saveObservedState(state); !durable {
			return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not persist current relay activation")}
		}
	}
	failClosed := ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessUnavailable, UnavailableSince: m.now().UTC(), ControllerAttemptID: prepared.attemptID}
	m.coordinator.Update(failClosed)
	if !state.Deactivation.RemoteDeleted {
		if err := generation.issuer.DeleteActivation(ctx, generation.license, state.Deactivation.ActivationID); err != nil && !issuerReportsMissingActivation(err) {
			return RelayStatus{}, managementIssuerError(err)
		}
		state.Deactivation.RemoteDeleted = true
		if durable, _ := m.saveObservedState(state); !durable {
			return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not checkpoint relay deactivation")}
		}
	}
	offPending := RelayManagedState{Mode: RelayModeOff, Generation: state.Generation + 1}
	intent := *state.Deactivation
	intent.StateGeneration = offPending.Generation
	offPending.Deactivation = &intent
	if state.Mode == RelayModeOff {
		offPending = state
	}
	if durable, _ := m.saveObservedState(offPending); !durable {
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not checkpoint relay off state")}
	}
	m.coordinator.Update(ResolvedRelay{RelayManagedState: offPending, Readiness: RelayReadinessOff})
	if err := m.licenses.Clear(ctx); err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("could not clear relay license")}
	}
	off := offPending
	off.Deactivation = nil
	if durable, _ := m.saveObservedState(off); !durable {
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("could not clear relay deactivation intent")}
	}
	next := ResolvedRelay{RelayManagedState: off, Readiness: RelayReadinessOff}
	m.coordinator.Update(next)
	if m.controller != nil && !m.controller.CommitGeneration(prepared) {
		return relayStatus(next, m.now), nil
	}
	return relayStatus(next, m.now), nil
}

func (m *RelayManager) saveObservedState(state RelayManagedState) (bool, error) {
	err := m.state.Save(state)
	if err == nil {
		return true, nil
	}
	var uncertain *RelayStateCommitError
	if !errors.As(err, &uncertain) {
		return false, err
	}
	observed, exists, loadErr := m.state.Load()
	if loadErr == nil && exists && relayManagedStatesEqual(observed, state) {
		return true, nil
	}
	return false, errors.Join(err, loadErr)
}

func relayManagedStatesEqual(left, right RelayManagedState) bool {
	if left.Mode != right.Mode || left.URL != right.URL || left.IssuerURL != right.IssuerURL || left.Label != right.Label || left.SessionID != right.SessionID || left.Generation != right.Generation {
		return false
	}
	if left.Deactivation == nil || right.Deactivation == nil {
		return left.Deactivation == nil && right.Deactivation == nil
	}
	return *left.Deactivation == *right.Deactivation
}

func issuerReportsMissingActivation(err error) bool {
	var issuerError *relay.IssuerError
	return errors.As(err, &issuerError) && issuerError.Status == http.StatusNotFound
}

func managementIssuerError(err error) error {
	var issuerError *relay.IssuerError
	if !errors.As(err, &issuerError) {
		return &RelayManagementError{Code: "issuer_unavailable", Err: errors.New("relay issuer request failed")}
	}
	code := string(issuerError.Kind)
	if code == "" {
		code = "issuer_unavailable"
	}
	return &RelayManagementError{Code: code, Err: fmt.Errorf("relay issuer request failed: %s", code)}
}
