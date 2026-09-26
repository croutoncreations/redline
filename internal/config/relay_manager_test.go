package config

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/relay"
)

type managerLicenseStore struct {
	mu            sync.Mutex
	value         string
	loads         int
	replacements  []string
	clears        int
	beforeReplace func()
	// loadErr, when set, is returned by the next Load call and then cleared.
	// This lets a test simulate exactly one transient Keychain failure.
	loadErr error
}

func (s *managerLicenseStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		err := s.loadErr
		s.loadErr = nil
		return "", err
	}
	if s.value == "" {
		return "", ErrLicenseNotFound
	}
	return s.value, nil
}
func (s *managerLicenseStore) Replace(_ context.Context, value string) error {
	if s.beforeReplace != nil {
		s.beforeReplace()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.replacements = append(s.replacements, value)
	return nil
}
func (s *managerLicenseStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = ""
	s.clears++
	return nil
}

type managerIssuer struct {
	entitlement    func(context.Context, string, string, string) (relay.ReceivedEntitlement, error)
	devices        []relay.Activation
	activationsErr error
	deleteErr      error
	deleted        []string
}

func (i *managerIssuer) Entitlement(ctx context.Context, key, sid, label string) (relay.ReceivedEntitlement, error) {
	return i.entitlement(ctx, key, sid, label)
}
func (i *managerIssuer) Activations(context.Context, string) ([]relay.Activation, error) {
	return append([]relay.Activation(nil), i.devices...), i.activationsErr
}
func (i *managerIssuer) DeleteActivation(_ context.Context, _ string, id string) error {
	i.deleted = append(i.deleted, id)
	return i.deleteErr
}
func (*managerIssuer) Portal(context.Context, string) (*url.URL, error) {
	return url.Parse("https://billing.example/short-lived")
}

func newTestRelayManager(t *testing.T, initial ResolvedRelay, licenses *managerLicenseStore, issuer RelayManagementIssuer, controller bool) (*RelayManager, context.CancelFunc) {
	t.Helper()
	state := NewRelayStateStore(t.TempDir() + "/relay-state.json")
	if err := state.Save(initial.RelayManagedState); err != nil {
		t.Fatal(err)
	}
	coordinator := NewRelayCoordinator(initial)
	var entitlement *EntitlementController
	if controller {
		entitlement = NewEntitlementController(EntitlementControllerOptions{
			Coordinator: coordinator, Initial: initial, Licenses: licenses, Issuer: issuer,
			Cache: relay.NewEntitlementCacheStore(t.TempDir() + "/relay-entitlement.json"),
		})
	}
	manager := NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, Issuer: issuer, Controller: entitlement,
		Coordinator: coordinator, AttemptTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if controller {
		go manager.Run(ctx)
	}
	return manager, cancel
}

func TestRelayManagerPureValidationPrecedesStores(t *testing.T) {
	licenses := &managerLicenseStore{value: "old-secret"}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeSelfHosted, URL: "http://not-secure", LicenseKey: "must-not-be-written"})
	var management *RelayManagementError
	if !errors.As(err, &management) || management.Code != "invalid_request" {
		t.Fatalf("error = %v", err)
	}
	if licenses.value != "old-secret" || len(licenses.replacements) != 0 || licenses.clears != 0 {
		t.Fatalf("invalid request touched secure store: %#v", licenses)
	}
}

func TestRelayManagerSelfHostedConnectionIsProvenNotConfigured(t *testing.T) {
	licenses := &managerLicenseStore{value: "old-secret"}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeSelfHosted, URL: "https://relay.example"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessSelfHosted || status.Connection != RelayConnecting {
		t.Fatalf("configured status = %#v", status)
	}
	manager.SetConnected(true)
	if got := manager.Status(); got.Connection != RelayConnected {
		t.Fatalf("connected status = %#v", got)
	}
	managed, exists, err := manager.state.Load()
	if err != nil || !exists || managed.SessionID == "" || managed.URL != "https://relay.example" {
		t.Fatalf("managed state = %#v exists=%v err=%v", managed, exists, err)
	}
	if licenses.value != "old-secret" || licenses.loads != 0 || licenses.clears != 0 {
		t.Fatalf("self-hosted setup touched hosted license: %#v", licenses)
	}
}

func TestRelayManagerPresentationOnlyChangePreservesLiveConnection(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret"}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, Label: "old", SessionID: "managed-session-abcdefghij"},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
		EntitlementToken: NewRelayEntitlementToken("runtime-secret"), ExpiresAt: time.Now().Add(time.Hour), ReconnectGeneration: 9,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, nil, false)
	defer cancel()
	if err := manager.state.Save(initial.RelayManagedState); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, Label: "new"})
	if err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if status.Connection != RelayConnected || !current.Connected || current.EntitlementToken.Value() != "runtime-secret" || current.ReconnectGeneration != 9 || current.Label != "new" {
		t.Fatalf("presentation change disturbed connection: status=%#v current=%#v", status, current)
	}
}

func TestRelayManagerConnectionCallbacksRequireMatchingNewestGeneration(t *testing.T) {
	licenses := &managerLicenseStore{}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://relay.example", SessionID: "managed-session-abcdefghij", Generation: 2},
		Readiness:         RelayReadinessSelfHosted, Dial: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, nil, false)
	defer cancel()
	first := RelayConnectionIdentity{URL: initial.URL, SessionID: initial.SessionID, ConnectionGeneration: 10}
	second := first
	second.ConnectionGeneration = 11
	manager.SetConnection(first, true)
	manager.SetConnection(second, false) // registers the replacement before it connects
	manager.SetConnection(first, true)   // delayed old connect
	if manager.Current().Connected {
		t.Fatal("delayed old connect changed the replacement connection")
	}
	manager.SetConnection(second, true)
	manager.SetConnection(first, false) // delayed old disconnect
	if !manager.Current().Connected {
		t.Fatal("delayed old disconnect cleared the replacement connection")
	}
	wrongRoute := second
	wrongRoute.ConnectionGeneration++
	wrongRoute.URL = "https://old.example"
	manager.SetConnection(wrongRoute, false)
	if !manager.Current().Connected {
		t.Fatal("wrong-route callback changed connection truth")
	}
}

func TestRelayCoordinatorControllerPublicationPreservesConcurrentConnection(t *testing.T) {
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 8},
		Readiness:         RelayReadinessHostedConfigured,
	}
	coordinator := NewRelayCoordinator(initial)
	staleController := initial
	staleController.Readiness = RelayReadinessActive
	staleController.Dial = true
	staleController.EntitlementToken = NewRelayEntitlementToken("authority")
	staleController.RenewsAt = time.Now().Add(time.Minute)
	staleController.ExpiresAt = time.Now().Add(time.Hour)
	coordinator.Modify(func(current ResolvedRelay) ResolvedRelay {
		current.Connected = true
		return current
	})
	coordinator.UpdateController(staleController)
	if current := coordinator.Current(); !current.Connected || current.Readiness != RelayReadinessActive {
		t.Fatalf("merged publication = %#v", current)
	}
	configurationChange := staleController
	configurationChange.Generation++
	coordinator.UpdateController(configurationChange)
	if coordinator.Current().Connected {
		t.Fatal("configuration generation carried connection truth forward")
	}
}

func TestRelayManagerHostedConfigureWaitsForFirstControllerAttempt(t *testing.T) {
	licenses := &managerLicenseStore{}
	issuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerInvalidKey, Status: 401}
	}}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, issuer, true)
	defer cancel()
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "rl_test_not-real", Label: "work mac"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessInvalidKey || status.Connection != RelayDisconnected {
		t.Fatalf("status = %#v", status)
	}
}

func TestRelayManagerDeactivateDeletesCurrentThenClearsLocalAuthority(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret"}
	issuer := &managerIssuer{
		entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable}
		},
		devices: []relay.Activation{
			{ID: "lost", FirstSeen: time.Unix(1, 0)},
			{ID: "current", FirstSeen: time.Unix(2, 0), Current: true},
		},
	}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij"},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	defer cancel()
	status, err := manager.Deactivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessOff || status.Mode != RelayModeOff || status.Connection != RelayDisconnected {
		t.Fatalf("status = %#v", status)
	}
	if len(issuer.deleted) != 1 || issuer.deleted[0] != "current" || licenses.value != "" {
		t.Fatalf("deleted=%v license=%q", issuer.deleted, licenses.value)
	}
}

func TestRelayManagerDeactivationFailurePersistsRetryableIntentAndStaysFailClosed(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret"}
	issuer := &managerIssuer{
		entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable}
		},
		devices:   []relay.Activation{{ID: "current-device", Current: true, FirstSeen: time.Unix(1, 0)}},
		deleteErr: &relay.IssuerError{Kind: relay.IssuerUnavailable, Status: http.StatusServiceUnavailable, Retryable: true},
	}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 4},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
		EntitlementToken: NewRelayEntitlementToken("runtime-authority"), RenewsAt: time.Now().Add(time.Minute), ExpiresAt: time.Now().Add(time.Hour),
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	defer cancel()
	if _, err := manager.Deactivate(context.Background()); err == nil {
		t.Fatal("deactivate succeeded despite issuer failure")
	}
	current := manager.Current()
	if current.CanDial() || current.Connected || current.Readiness != RelayReadinessUnavailable {
		t.Fatalf("failed deactivation left runtime authority: %#v", current)
	}
	state, exists, err := manager.state.Load()
	if err != nil || !exists || state.Deactivation == nil || state.Deactivation.ActivationID != "current-device" || state.Deactivation.RemoteDeleted {
		t.Fatalf("durable intent = %#v exists=%v err=%v", state.Deactivation, exists, err)
	}
	if licenses.value != "hosted-secret" {
		t.Fatalf("retry credential was cleared: %q", licenses.value)
	}
	issuer.deleteErr = nil
	status, err := manager.Deactivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessOff || licenses.value != "" {
		t.Fatalf("retry status=%#v key=%q", status, licenses.value)
	}
	state, _, err = manager.state.Load()
	if err != nil || state.Mode != RelayModeOff || state.Deactivation != nil {
		t.Fatalf("completed state = %#v err=%v", state, err)
	}
}

func TestRelayManagerDeactivationRequiresExactlyOneCurrentActivation(t *testing.T) {
	// Two conflicting "current" activations remain a hard, non-idempotent
	// error: the manager cannot know which one to delete.
	licenses := &managerLicenseStore{value: "hosted-secret"}
	issuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		return relay.ReceivedEntitlement{}, nil
	}, devices: []relay.Activation{{ID: "one-current", Current: true, FirstSeen: time.Unix(1, 0)}, {ID: "two-current", Current: true, FirstSeen: time.Unix(2, 0)}}}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 1},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	_, err := manager.Deactivate(context.Background())
	cancel()
	if err == nil || manager.Current().CanDial() || len(issuer.deleted) != 0 || licenses.value == "" {
		t.Fatalf("err=%v current=%#v deleted=%v key=%q", err, manager.Current(), issuer.deleted, licenses.value)
	}
}

// TestRelayManagerDeactivationCompletesIdempotentlyWhenIssuerHasNoCurrentActivation
// proves that a remotely-absent current activation completes the durable off
// transition instead of returning an error that would strand the intent
// forever (every retry would re-discover the same empty list and never clear
// the local credential or generation barrier).
func TestRelayManagerDeactivationCompletesIdempotentlyWhenIssuerHasNoCurrentActivation(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret"}
	issuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		return relay.ReceivedEntitlement{}, nil
	}, devices: []relay.Activation{{ID: "other-device", FirstSeen: time.Unix(1, 0)}}}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 1},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	defer cancel()
	status, err := manager.Deactivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessOff || status.Mode != RelayModeOff {
		t.Fatalf("status = %#v", status)
	}
	if len(issuer.deleted) != 0 || licenses.value != "" {
		t.Fatalf("deleted=%v license=%q", issuer.deleted, licenses.value)
	}
	state, exists, err := manager.state.Load()
	if err != nil || !exists || state.Mode != RelayModeOff || state.Deactivation != nil {
		t.Fatalf("final state = %#v exists=%v err=%v", state, exists, err)
	}
	// The completed intent must not block reconfiguration afterward.
	if _, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeSelfHosted, URL: "https://relay.example"}); err != nil {
		t.Fatalf("reconfigure after idempotent deactivation: %v", err)
	}
}

// TestRelayManagerRunGatesControllerBeforeResolvingPendingDeactivation proves
// a pending durable deactivation intent blocks the controller from making any
// entitlement/issuer call even when Deactivate's own credential/state load
// fails transiently (e.g. one Keychain hiccup at startup). Run must establish
// the controller generation barrier unconditionally before attempting the
// fallible retry, so a single transient failure can never leave the
// controller free to renew and re-register the SID while the durable intent
// remains unresolved.
func TestRelayManagerRunGatesControllerBeforeResolvingPendingDeactivation(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret", loadErr: ErrLicenseStoreUnavailable}
	var entitlementCalls int32
	issuer := &managerIssuer{
		entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			atomic.AddInt32(&entitlementCalls, 1)
			return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
		},
	}
	intent := &RelayDeactivationIntent{StateGeneration: 5, DiscoverCurrent: true}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 5, Deactivation: intent},
		Readiness:         RelayReadinessUnavailable,
	}
	state := NewRelayStateStore(t.TempDir() + "/relay-state.json")
	if err := state.Save(initial.RelayManagedState); err != nil {
		t.Fatal(err)
	}
	coordinator := NewRelayCoordinator(initial)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: initial, Licenses: licenses, Issuer: issuer,
		Cache: relay.NewEntitlementCacheStore(t.TempDir() + "/relay-entitlement.json"),
	})
	manager := NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, Issuer: issuer, Controller: controller,
		Coordinator: coordinator, AttemptTimeout: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Run's one-shot Deactivate attempt fails (the injected loadErr is
	// consumed and cleared), leaving the durable intent unresolved. The
	// controller must remain gated for the rest of the process lifetime
	// regardless: nothing else in Run ever calls CommitGeneration for it.
	go manager.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&entitlementCalls) != 0 {
		t.Fatalf("controller issued entitlement calls while deactivation intent was pending: %d calls", entitlementCalls)
	}
	if manager.Current().CanDial() {
		t.Fatal("runtime remained dialable while deactivation intent was pending")
	}
	state2, exists, err := manager.state.Load()
	if err != nil || !exists || state2.Deactivation == nil {
		t.Fatalf("intent was lost after transient load failure: state=%#v exists=%v err=%v", state2, exists, err)
	}
}

func TestRelayManagerCompletesRemoteDeletedIntentWithoutCredential(t *testing.T) {
	licenses := &managerLicenseStore{}
	intent := &RelayDeactivationIntent{ActivationID: "current-device", StateGeneration: 5, RemoteDeleted: true}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 5, Deactivation: intent},
		Readiness:         RelayReadinessUnavailable,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, nil, false)
	defer cancel()
	status, err := manager.Deactivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessOff || status.Mode != RelayModeOff {
		t.Fatalf("status = %#v", status)
	}
	state, _, err := manager.state.Load()
	if err != nil || state.Mode != RelayModeOff || state.Deactivation != nil {
		t.Fatalf("state = %#v err=%v", state, err)
	}
}

func TestRelayManagerDeactivationDiscoveryFailureBlocksReregistration(t *testing.T) {
	licenses := &managerLicenseStore{value: "hosted-secret"}
	issuer := &managerIssuer{
		entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
			return relay.ReceivedEntitlement{}, nil
		},
		activationsErr: &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true},
	}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 2},
		Readiness:         RelayReadinessActive, Dial: true, Connected: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	defer cancel()
	if _, err := manager.Deactivate(context.Background()); err == nil {
		t.Fatal("deactivation discovery unexpectedly succeeded")
	}
	state, _, err := manager.state.Load()
	if err != nil || state.Deactivation == nil || !state.Deactivation.DiscoverCurrent || state.Deactivation.ActivationID != "" {
		t.Fatalf("discovery intent = %#v err=%v", state.Deactivation, err)
	}
	if _, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "replacement-key"}); err == nil {
		t.Fatal("pending deactivation allowed re-registration")
	}
	if licenses.value != "hosted-secret" || manager.Current().CanDial() {
		t.Fatalf("pending state key=%q runtime=%#v", licenses.value, manager.Current())
	}
}

func TestRelayManagerCompensatesSecureStoreWhenStateCommitFails(t *testing.T) {
	licenses := &managerLicenseStore{value: "old-secret"}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	previousHook := relayStateBeforeRename
	relayStateBeforeRename = func() error { return errors.New("injected commit failure") }
	t.Cleanup(func() { relayStateBeforeRename = previousHook })
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "new-secret"})
	if err == nil {
		t.Fatal("configure succeeded")
	}
	if licenses.value != "old-secret" {
		t.Fatalf("secure store was not compensated: %q", licenses.value)
	}
}

func TestRelayManagerReloadsDurableStateAfterCommitUncertainty(t *testing.T) {
	licenses := &managerLicenseStore{value: "old-secret"}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	previousHook := relayStateAfterRename
	relayStateAfterRename = func() error { return errors.New("injected directory sync uncertainty") }
	t.Cleanup(func() { relayStateAfterRename = previousHook })
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "new-secret"})
	if err != nil {
		t.Fatalf("observed durable commit returned failure: %v", err)
	}
	state, exists, loadErr := manager.state.Load()
	if loadErr != nil || !exists || state.Mode != RelayModeHosted || licenses.value != "new-secret" || status.Mode != RelayModeHosted {
		t.Fatalf("state=%#v exists=%v loadErr=%v key=%q status=%#v", state, exists, loadErr, licenses.value, status)
	}
}

func TestRelayManagerDrainsOldAttemptBeforeReplacingCredential(t *testing.T) {
	started := make(chan struct{})
	drained := make(chan struct{})
	var once sync.Once
	issuer := &managerIssuer{entitlement: func(ctx context.Context, key, _, _ string) (relay.ReceivedEntitlement, error) {
		if key == "old-key" {
			once.Do(func() { close(started) })
			<-ctx.Done()
			close(drained)
			return relay.ReceivedEntitlement{}, ctx.Err()
		}
		return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerInvalidKey, Status: http.StatusUnauthorized}
	}}
	licenses := &managerLicenseStore{value: "old-key"}
	licenses.beforeReplace = func() {
		select {
		case <-drained:
		default:
			t.Fatal("credential was replaced before the old controller attempt drained")
		}
	}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: DefaultIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 1},
		Readiness:         RelayReadinessHostedConfigured,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, true)
	defer cancel()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old controller attempt did not start")
	}
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "new-key"})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != RelayReadinessInvalidKey {
		t.Fatalf("status = %#v", status)
	}
}

func TestRelayManagerIssuerSelectionFollowsCommittedRuntimeGeneration(t *testing.T) {
	type issuerRequest struct {
		LicenseKey string `json:"license_key"`
		SID        string `json:"sid"`
		Label      string `json:"label"`
	}
	oldRequests := make(chan issuerRequest, 4)
	newRequests := make(chan issuerRequest, 4)
	serve := func(requests chan<- issuerRequest) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request issuerRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode issuer request: %v", err)
			}
			requests <- request
			w.WriteHeader(http.StatusUnauthorized)
		}))
	}
	oldServer := serve(oldRequests)
	defer oldServer.Close()
	newServer := serve(newRequests)
	defer newServer.Close()
	issuerHTTP := func(server *httptest.Server) *http.Client {
		client := server.Client()
		transport := client.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig.InsecureSkipVerify = true // test-only TLS issuers
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}
		client.Transport = transport
		return client
	}
	oldIssuerURL := "https://old-issuer.example"
	oldIssuer, err := relay.NewIssuerClient(oldIssuerURL, issuerHTTP(oldServer))
	if err != nil {
		t.Fatal(err)
	}
	newIssuer, err := relay.NewIssuerClient(DefaultIssuerURL, issuerHTTP(newServer))
	if err != nil {
		t.Fatal(err)
	}
	factory := RelayIssuerFactory(func(issuerURL string) (RelayManagementIssuer, error) {
		if issuerURL == oldIssuerURL {
			return oldIssuer, nil
		}
		if issuerURL == DefaultIssuerURL {
			return newIssuer, nil
		}
		return nil, errors.New("unexpected issuer generation")
	})
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: oldIssuerURL, Label: "old-label", SessionID: "managed-session-abcdefghij", Generation: 3},
		Readiness:         RelayReadinessHostedConfigured,
	}
	licenses := &managerLicenseStore{value: "old-key"}
	state := NewRelayStateStore(t.TempDir() + "/relay-state.json")
	if err := state.Save(initial.RelayManagedState); err != nil {
		t.Fatal(err)
	}
	coordinator := NewRelayCoordinator(initial)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: initial, Licenses: licenses, IssuerFactory: factory,
		Cache: relay.NewEntitlementCacheStore(t.TempDir() + "/relay-entitlement.json"),
	})
	manager := NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, IssuerFactory: factory, Controller: controller,
		Coordinator: coordinator, AttemptTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.Run(ctx)
	select {
	case request := <-oldRequests:
		if request.LicenseKey != "old-key" || request.Label != "old-label" {
			t.Fatalf("old issuer request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("old issuer was not attempted")
	}
	status, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "sentinel-new-key", Label: "new-label"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-newRequests:
		if request.LicenseKey != "sentinel-new-key" || request.Label != "new-label" || request.SID == "" {
			t.Fatalf("new issuer request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatalf("new issuer was not attempted; status=%#v current=%#v", status, manager.Current())
	}
	if status.State != RelayReadinessInvalidKey {
		t.Fatalf("status = %#v current=%#v", status, manager.Current())
	}
	select {
	case request := <-oldRequests:
		if request.LicenseKey == "sentinel-new-key" || request.Label == "new-label" {
			t.Fatalf("new generation reached old issuer: %#v", request)
		}
	default:
	}
}

// TestRelayManagerConfigureNeverReusesCredentialAcrossIssuerChangeWithoutKey
// proves the reviewed Critical finding is fixed: a hosted configure that
// omits license_key must never let a credential bound to one issuer reach a
// different issuer, whether by explicit custom issuer_url or by the default
// hosted issuer following a prior non-hosted (self_hosted/off) generation.
func TestRelayManagerConfigureNeverReusesCredentialAcrossIssuerChangeWithoutKey(t *testing.T) {
	sentinel := "sentinel-must-never-cross-issuers"
	licenses := &managerLicenseStore{value: sentinel}
	issuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerInvalidKey, Status: http.StatusUnauthorized}
	}}
	// Case 1: previous generation is self_hosted (no issuer binding at all).
	// Switching straight to hosted without a key must fail closed rather than
	// silently sending the leftover Keychain credential to the default issuer.
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeSelfHosted, URL: "https://relay.example", SessionID: "managed-session-abcdefghij"},
		Readiness:         RelayReadinessSelfHosted, Dial: true,
	}
	manager, cancel := newTestRelayManager(t, initial, licenses, issuer, false)
	defer cancel()
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted})
	var management *RelayManagementError
	if !errors.As(err, &management) || management.Code != "license_key_required" {
		t.Fatalf("self_hosted->hosted without key: err=%v", err)
	}
	if licenses.value != sentinel {
		t.Fatalf("credential mutated: %q", licenses.value)
	}
}

// TestRelayManagerConfigurePreservesIssuerOnPresentationOnlyReuse proves the
// fix does not regress the legitimate case: reusing a credential already
// bound to the current hosted issuer generation (label-only change) must
// still work without requiring a fresh key.
func TestRelayManagerConfigurePreservesIssuerOnPresentationOnlyReuse(t *testing.T) {
	var mu sync.Mutex
	var seenIssuer string
	customIssuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerInvalidKey, Status: http.StatusUnauthorized}
	}}
	factory := RelayIssuerFactory(func(issuerURL string) (RelayManagementIssuer, error) {
		mu.Lock()
		seenIssuer = issuerURL
		mu.Unlock()
		return customIssuer, nil
	})
	const customIssuerURL = "https://custom-issuer.example"
	licenses := &managerLicenseStore{value: "bound-key"}
	initial := ResolvedRelay{
		RelayManagedState: RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: customIssuerURL, SessionID: "managed-session-abcdefghij", Generation: 1},
		Readiness:         RelayReadinessHostedConfigured,
	}
	state := NewRelayStateStore(t.TempDir() + "/relay-state.json")
	if err := state.Save(initial.RelayManagedState); err != nil {
		t.Fatal(err)
	}
	coordinator := NewRelayCoordinator(initial)
	controller := NewEntitlementController(EntitlementControllerOptions{
		Coordinator: coordinator, Initial: initial, Licenses: licenses, IssuerFactory: factory,
		Cache: relay.NewEntitlementCacheStore(t.TempDir() + "/relay-entitlement.json"),
	})
	manager := NewRelayManager(RelayManagerOptions{
		State: state, Licenses: licenses, IssuerFactory: factory, Controller: controller,
		Coordinator: coordinator, AttemptTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Hosted mode without a new key, with issuer_url already the current
	// generation's custom issuer, must reuse the credential against that same
	// issuer (this is the legitimate presentation-only case).
	// A presentation-only change (label only) is not an authority change, so
	// Configure returns synchronously without waiting on the controller. The
	// internal hosted_configured readiness maps to public "unavailable" until
	// renewal establishes real status; that mapping is unrelated to this test.
	if _, err := manager.Configure(ctx, RelayConfigureRequest{Mode: RelayModeHosted, Label: "relabel"}); err != nil {
		t.Fatal(err)
	}
	go manager.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if seenIssuer != customIssuerURL {
		t.Fatalf("issuer selection changed on presentation-only reuse: %q", seenIssuer)
	}
}

func TestRelayManagerHostedTimeoutReturnsPendingAndCompletionRemainsObservable(t *testing.T) {
	release := make(chan struct{})
	issuer := &managerIssuer{entitlement: func(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
		<-release
		return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerInvalidKey, Status: http.StatusUnauthorized}
	}}
	licenses := &managerLicenseStore{}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, issuer, true)
	defer cancel()
	manager.timeout = 20 * time.Millisecond
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "pending-key"})
	var management *RelayManagementError
	if !errors.As(err, &management) || management.Code != "pending" {
		t.Fatalf("error = %v", err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for manager.Status().State != RelayReadinessInvalidKey && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := manager.Status(); got.State != RelayReadinessInvalidKey {
		t.Fatalf("later status = %#v", got)
	}
}

func TestRelayManagerCloseAdmissionDrainsAdmittedMutation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	licenses := &managerLicenseStore{}
	licenses.beforeReplace = func() { close(entered); <-release }
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	configured := make(chan error, 1)
	go func() {
		_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "admitted-key"})
		configured <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("mutation did not reach secure-store boundary")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.CloseAdmission(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		manager.lifecycleMu.Lock()
		closing := manager.lifecycle != relayManagerOpen
		manager.lifecycleMu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closing gate was not established")
		}
		time.Sleep(time.Millisecond)
	}
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeOff})
	var management *RelayManagementError
	if !errors.As(err, &management) || management.Code != "management_closing" {
		t.Fatalf("closing gate did not reject new mutation: %v", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("close returned before admitted mutation drained: %v", err)
	default:
	}
	close(release)
	if err := <-configured; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestRelayManagerClosingRejectsMutationBeforePersistence(t *testing.T) {
	licenses := &managerLicenseStore{}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	if err := manager.CloseAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeHosted, LicenseKey: "must-not-persist"})
	var management *RelayManagementError
	if !errors.As(err, &management) || management.Code != "management_closing" {
		t.Fatalf("error = %v", err)
	}
	if len(licenses.replacements) != 0 {
		t.Fatalf("closing mutation reached Keychain: %#v", licenses.replacements)
	}
}

func TestRelayManagerSerializesConcurrentMutations(t *testing.T) {
	licenses := &managerLicenseStore{}
	manager, cancel := newTestRelayManager(t, ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, licenses, nil, false)
	defer cancel()
	var wg sync.WaitGroup
	for _, relayURL := range []string{"https://one.example", "https://two.example"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.Configure(context.Background(), RelayConfigureRequest{Mode: RelayModeSelfHosted, URL: relayURL}); err != nil {
				t.Errorf("configure: %v", err)
			}
		}()
	}
	wg.Wait()
	managed, _, err := manager.state.Load()
	if err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if managed.URL != current.URL || managed.SessionID != current.SessionID {
		t.Fatalf("persisted/runtime split: managed=%#v current=%#v", managed, current)
	}
}
