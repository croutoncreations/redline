package config

import (
	"context"
	"errors"
	"fmt"
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
	Controller     *EntitlementController
	Coordinator    *RelayCoordinator
	AttemptTimeout time.Duration
	Now            func() time.Time
}

// RelayManager is the service's sole relay management authority. API handlers
// and CLI commands can ask it for operations but cannot reach Keychain, issuer,
// controller, state files, or runtime mutation independently.
type RelayManager struct {
	state       *RelayStateStore
	licenses    LicenseStore
	issuer      RelayManagementIssuer
	controller  *EntitlementController
	coordinator *RelayCoordinator
	timeout     time.Duration
	now         func() time.Time
	mutations   sync.Mutex
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
	issuerURL := resolved.IssuerURL
	if issuerURL == "" {
		issuerURL = DefaultIssuerURL
	}
	issuer, err := relay.NewIssuerClient(issuerURL, nil)
	if err != nil {
		return nil, fmt.Errorf("relay issuer: %w", err)
	}
	coordinator := NewRelayCoordinator(resolved)
	identityPath := relay.DefaultKeypairPath(cfg.Relay.KeypairPath, cfg.Database)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator,
		Initial:     resolved,
		Licenses:    licenses,
		Issuer:      issuer,
		Cache:       relay.NewEntitlementCacheStore(relay.DefaultEntitlementCachePath(identityPath)),
		Revocations: relay.NewEntitlementRevocationStore(relay.DefaultEntitlementRevocationPath(relay.DefaultEntitlementCachePath(identityPath))),
	})
	return NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, Issuer: issuer,
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
		state: opts.State, licenses: opts.Licenses, issuer: opts.Issuer,
		controller: opts.Controller, coordinator: opts.Coordinator,
		timeout: opts.AttemptTimeout, now: opts.Now,
	}
}

func (m *RelayManager) Current() ResolvedRelay { return m.coordinator.Current() }
func (m *RelayManager) Subscribe() (<-chan ResolvedRelay, func()) {
	return m.coordinator.Subscribe()
}
func (m *RelayManager) Run(ctx context.Context) {
	if m.controller != nil {
		m.controller.Run(ctx)
		return
	}
	<-ctx.Done()
}

// SetConnected is called only by the dialer lifecycle. Atomic modification
// prevents a disconnect racing a renewal from restoring stale authority.
func (m *RelayManager) TriggerRelayEntitlement(signal relay.EntitlementSignal) {
	if m.controller != nil {
		m.controller.TriggerRelayEntitlement(signal)
	}
}

func (m *RelayManager) SetConnected(connected bool) {
	m.coordinator.Modify(func(current ResolvedRelay) ResolvedRelay {
		current.Connected = connected && current.CanDial()
		return current
	})
}

func (m *RelayManager) Status() RelayStatus { return relayStatus(m.coordinator.Current()) }

func relayStatus(current ResolvedRelay) RelayStatus {
	state := current.Readiness
	if state == RelayReadinessHostedConfigured {
		// hosted_configured is an internal controller staging value, not part of
		// the public state union. Until the first issuer decision it is safely
		// presented as unavailable.
		state = RelayReadinessUnavailable
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
	if !current.RenewsAt.IsZero() {
		value := current.RenewsAt
		status.RenewsAt = &value
	}
	if !current.ExpiresAt.IsZero() {
		value := current.ExpiresAt
		status.ExpiresAt = &value
	}
	if !current.UnavailableSince.IsZero() {
		value := current.UnavailableSince
		status.Since = &value
	}
	if current.Readiness == RelayReadinessNoSeat {
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
	m.mutations.Lock()
	defer m.mutations.Unlock()
	return m.configureLocked(ctx, request, false)
}

func (m *RelayManager) configureLocked(ctx context.Context, request RelayConfigureRequest, clearLicense bool) (RelayStatus, error) {
	previousState, _, err := m.state.Load()
	if err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("relay state is unavailable")}
	}
	var previousLicense string
	var licenseErr error
	licenseStoreAccessible := true
	hadLicense := false
	if request.Mode == RelayModeHosted || clearLicense {
		previousLicense, licenseErr = m.licenses.Load(ctx)
		licenseStoreAccessible = licenseErr == nil || errors.Is(licenseErr, ErrLicenseNotFound)
		if !licenseStoreAccessible {
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
		nextState.URL, nextState.IssuerURL = DefaultHostedRelayURL, DefaultIssuerURL
	} else if request.Mode == RelayModeSelfHosted {
		nextState.URL = request.URL
	}
	if err := validateRelayManagedState(nextState); err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "invalid_request", Err: err}
	}

	licenseChanged := false
	licenseMutated := false
	if request.Mode == RelayModeHosted && request.LicenseKey != "" {
		licenseChanged = !hadLicense || previousLicense != request.LicenseKey
		err = m.licenses.Replace(ctx, request.LicenseKey)
		licenseMutated = err == nil
	} else if clearLicense {
		licenseChanged = hadLicense
		err = m.licenses.Clear(ctx)
		licenseMutated = err == nil
	}
	if err != nil {
		return RelayStatus{}, &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("relay secure store update failed")}
	}
	if err = m.state.Save(nextState); err != nil {
		var compensation error
		if licenseMutated {
			compensation = m.restoreLicense(ctx, previousLicense, hadLicense)
		}
		if compensation != nil {
			return RelayStatus{}, &RelayManagementError{Code: "compensation_failed", Err: errors.New("relay state update failed and secure store compensation failed")}
		}
		return RelayStatus{}, &RelayManagementError{Code: "state_unavailable", Err: errors.New("relay state update failed")}
	}

	previous := m.coordinator.Current()
	changedAuthority := previous.Mode != nextState.Mode || previous.URL != nextState.URL || previous.SessionID != nextState.SessionID || licenseChanged || request.LicenseKey != ""
	next := ResolvedRelay{RelayManagedState: nextState}
	switch nextState.Mode {
	case RelayModeOff:
		next.Readiness = RelayReadinessOff
	case RelayModeSelfHosted:
		next.Readiness, next.Dial = RelayReadinessSelfHosted, true
	case RelayModeHosted:
		if request.LicenseKey != "" || hadLicense {
			next.Readiness = RelayReadinessHostedConfigured
		} else {
			next.Readiness = RelayReadinessNeedsLicense
		}
	}
	if !changedAuthority {
		next = previous
		next.RelayManagedState = nextState
	}
	m.coordinator.Update(next)
	if m.controller == nil || !changedAuthority {
		return relayStatus(next), nil
	}
	updates, unsubscribe := m.coordinator.Subscribe()
	defer unsubscribe()
	if !m.controller.TriggerLicenseChanged() {
		return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
	}
	if nextState.Mode != RelayModeHosted || next.Readiness == RelayReadinessNeedsLicense {
		// The controller's generation barrier publishes fail-closed unavailable
		// while revoking old authority. Reassert the already durable non-hosted
		// or needs-license state so the API never reports that internal staging
		// transition as the configure result.
		m.coordinator.Update(next)
		return relayStatus(next), nil
	}
	return m.waitForHostedAttempt(ctx, updates, true)
}

func (m *RelayManager) waitForHostedAttempt(ctx context.Context, updates <-chan ResolvedRelay, skipRevocation bool) (RelayStatus, error) {
	waitCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	for {
		select {
		case current, ok := <-updates:
			if !ok {
				return RelayStatus{}, &RelayManagementError{Code: "controller_unavailable", Err: errors.New("relay controller is unavailable")}
			}
			// TriggerLicenseChanged first publishes fail-closed unavailable while
			// revoking the old credential generation. That transition is not the
			// synchronous issuer result the configure caller is waiting for.
			if skipRevocation && current.Readiness == RelayReadinessUnavailable {
				skipRevocation = false
				continue
			}
			if current.Readiness != RelayReadinessHostedConfigured {
				return relayStatus(current), nil
			}
		case <-waitCtx.Done():
			current := m.coordinator.Modify(func(current ResolvedRelay) ResolvedRelay {
				if current.Readiness == RelayReadinessHostedConfigured {
					current.Readiness = RelayReadinessUnavailable
					current.UnavailableSince = m.now()
					current.Dial = false
				}
				return current
			})
			return relayStatus(current), nil
		}
	}
}

func (m *RelayManager) restoreLicense(ctx context.Context, previous string, existed bool) error {
	if existed {
		return m.licenses.Replace(ctx, previous)
	}
	return m.licenses.Clear(ctx)
}

func (m *RelayManager) Devices(ctx context.Context) ([]relay.Activation, error) {
	license, err := m.loadLicense(ctx)
	if err != nil {
		return nil, err
	}
	activations, err := m.issuer.Activations(ctx, license)
	if err != nil {
		return nil, managementIssuerError(err)
	}
	return activations, nil
}

func (m *RelayManager) DeactivateDevice(ctx context.Context, id string) error {
	if id == "" || len(id) > 512 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return &RelayManagementError{Code: "invalid_request", Err: errors.New("activation id is malformed")}
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	license, err := m.loadLicense(ctx)
	if err != nil {
		return err
	}
	if err := m.issuer.DeleteActivation(ctx, license, id); err != nil {
		return managementIssuerError(err)
	}
	return nil
}

func (m *RelayManager) Portal(ctx context.Context) (*url.URL, error) {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	license, err := m.loadLicense(ctx)
	if err != nil {
		return nil, err
	}
	portal, err := m.issuer.Portal(ctx, license)
	if err != nil {
		return nil, managementIssuerError(err)
	}
	return portal, nil
}

func (m *RelayManager) Deactivate(ctx context.Context) (RelayStatus, error) {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	license, err := m.loadLicense(ctx)
	if err != nil {
		return RelayStatus{}, err
	}
	activations, err := m.issuer.Activations(ctx, license)
	if err != nil {
		return RelayStatus{}, managementIssuerError(err)
	}
	for _, activation := range activations {
		if activation.Current {
			if err := m.issuer.DeleteActivation(ctx, license, activation.ID); err != nil {
				return RelayStatus{}, managementIssuerError(err)
			}
			break
		}
	}
	return m.configureLocked(ctx, RelayConfigureRequest{Mode: RelayModeOff}, true)
}

func (m *RelayManager) loadLicense(ctx context.Context) (string, error) {
	if m.issuer == nil {
		return "", &RelayManagementError{Code: "issuer_unavailable", Err: errors.New("relay issuer is unavailable")}
	}
	license, err := m.licenses.Load(ctx)
	if errors.Is(err, ErrLicenseNotFound) || (err == nil && license == "") {
		return "", &RelayManagementError{Code: "needs_license", Err: errors.New("hosted relay license is required")}
	}
	if err != nil {
		return "", &RelayManagementError{Code: "secure_store_unavailable", Err: errors.New("relay secure store is unavailable")}
	}
	return license, nil
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
