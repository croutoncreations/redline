package config

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jfox/redline/internal/relay"
)

type managerLicenseStore struct {
	mu           sync.Mutex
	value        string
	loads        int
	replacements []string
	clears       int
}

func (s *managerLicenseStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.value == "" {
		return "", ErrLicenseNotFound
	}
	return s.value, nil
}
func (s *managerLicenseStore) Replace(_ context.Context, value string) error {
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
	entitlement func(context.Context, string, string, string) (relay.ReceivedEntitlement, error)
	devices     []relay.Activation
	deleted     []string
}

func (i *managerIssuer) Entitlement(ctx context.Context, key, sid, label string) (relay.ReceivedEntitlement, error) {
	return i.entitlement(ctx, key, sid, label)
}
func (i *managerIssuer) Activations(context.Context, string) ([]relay.Activation, error) {
	return append([]relay.Activation(nil), i.devices...), nil
}
func (i *managerIssuer) DeleteActivation(_ context.Context, _ string, id string) error {
	i.deleted = append(i.deleted, id)
	return nil
}
func (*managerIssuer) Portal(context.Context, string) (*url.URL, error) {
	return url.Parse("https://billing.example/short-lived")
}

func newTestRelayManager(t *testing.T, initial ResolvedRelay, licenses *managerLicenseStore, issuer RelayManagementIssuer, controller bool) (*RelayManager, context.CancelFunc) {
	t.Helper()
	state := NewRelayStateStore(t.TempDir() + "/relay-state.json")
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
