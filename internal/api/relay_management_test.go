package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/relay"
	"github.com/jfox/redline/internal/store"
)

type apiRelayLicenses struct {
	mu    sync.Mutex
	value string
}

func (s *apiRelayLicenses) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == "" {
		return "", config.ErrLicenseNotFound
	}
	return s.value, nil
}
func (s *apiRelayLicenses) Replace(_ context.Context, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}
func (s *apiRelayLicenses) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = ""
	return nil
}

type apiRelayIssuer struct {
	mu          sync.Mutex
	portalCalls int
	deleted     []string
}

func (*apiRelayIssuer) Entitlement(context.Context, string, string, string) (relay.ReceivedEntitlement, error) {
	return relay.ReceivedEntitlement{}, &relay.IssuerError{Kind: relay.IssuerUnavailable, Retryable: true}
}
func (*apiRelayIssuer) Activations(context.Context, string) ([]relay.Activation, error) {
	return []relay.Activation{{ID: "opaque-device-id", Label: "desk", FirstSeen: time.Unix(10, 0).UTC(), Current: true}}, nil
}
func (i *apiRelayIssuer) DeleteActivation(_ context.Context, _ string, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.deleted = append(i.deleted, id)
	return nil
}
func (i *apiRelayIssuer) Portal(context.Context, string) (*url.URL, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.portalCalls++
	return url.Parse("https://billing.example/portal/" + string(rune('0'+i.portalCalls)))
}

func relayManagementHandler(t *testing.T, initial config.ResolvedRelay, licenses *apiRelayLicenses, issuer *apiRelayIssuer) http.Handler {
	t.Helper()
	cfg := testConfig("http://unused")
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	cfg.Database = filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manager := config.NewRelayManager(config.RelayManagerOptions{
		State:    config.NewRelayStateStore(filepath.Join(t.TempDir(), "relay-state.json")),
		Licenses: licenses, Issuer: issuer, Coordinator: config.NewRelayCoordinator(initial),
	})
	return api.NewServerWithRelayManager(cfg, db, time.Now, manager)
}

func relayRequest(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "http://127.0.0.1:7436"+path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestRelayManagementAPIIsAuthenticatedAndStatusIsStrictlyNonSecret(t *testing.T) {
	const secret = "rl_test_never-return-this"
	initial := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL, IssuerURL: config.DefaultIssuerURL, Label: "private label", SessionID: "private-session-123456"},
		Readiness:         config.RelayReadinessActive, Dial: true, Connected: true,
		EntitlementToken: config.NewRelayEntitlementToken("private-entitlement"), Seats: 2, SeatsUsed: 1, MaxClients: 5,
	}
	handler := relayManagementHandler(t, initial, &apiRelayLicenses{value: secret}, &apiRelayIssuer{})
	if got := relayRequest(t, handler, http.MethodGet, "/v1/relay/status", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", got.Code)
	}
	got := relayRequest(t, handler, http.MethodGet, "/v1/relay/status", "test-token-that-is-at-least-thirty-two-characters", "")
	if got.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
	for _, forbidden := range []string{secret, "private-entitlement", "private-session", "issuer_url", "license_key", "label"} {
		if strings.Contains(got.Body.String(), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, got.Body.String())
		}
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "active" || body["connection"] != "connected" || body["seats"] != float64(2) {
		t.Fatalf("status body = %v", body)
	}
}

func TestRelayManagementAPIConfigureAndIssuerOperationsNeverEchoSecrets(t *testing.T) {
	const secret = "rl_test_api_secret"
	initial := config.ResolvedRelay{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff}, Readiness: config.RelayReadinessOff}
	licenses := &apiRelayLicenses{value: secret}
	issuer := &apiRelayIssuer{}
	handler := relayManagementHandler(t, initial, licenses, issuer)
	token := "test-token-that-is-at-least-thirty-two-characters"

	configured := relayRequest(t, handler, http.MethodPost, "/v1/relay/configure", token, `{"mode":"self_hosted","url":"https://relay.example"}`)
	if configured.Code != http.StatusOK || strings.Contains(configured.Body.String(), secret) {
		t.Fatalf("configure status=%d body=%s", configured.Code, configured.Body.String())
	}
	// Self-hosted setup does not read or discard a previously stored hosted key.
	devices := relayRequest(t, handler, http.MethodGet, "/v1/relay/devices", token, "")
	if devices.Code != http.StatusOK || strings.Contains(devices.Body.String(), secret) || !strings.Contains(devices.Body.String(), "opaque-device-id") {
		t.Fatalf("devices status=%d body=%s", devices.Code, devices.Body.String())
	}
	deleted := relayRequest(t, handler, http.MethodDelete, "/v1/relay/devices/opaque-device-id", token, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	first := relayRequest(t, handler, http.MethodPost, "/v1/relay/portal", token, `{}`)
	second := relayRequest(t, handler, http.MethodPost, "/v1/relay/portal", token, `{}`)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("portal responses were not fresh: %s %s", first.Body.String(), second.Body.String())
	}
	deactivated := relayRequest(t, handler, http.MethodPost, "/v1/relay/deactivate", token, `{}`)
	if deactivated.Code != http.StatusOK || !strings.Contains(deactivated.Body.String(), `"state":"off"`) {
		t.Fatalf("deactivate status=%d body=%s", deactivated.Code, deactivated.Body.String())
	}
	if len(issuer.deleted) != 2 || issuer.deleted[0] != "opaque-device-id" || issuer.deleted[1] != "opaque-device-id" {
		t.Fatalf("deleted = %v", issuer.deleted)
	}
	if _, err := licenses.Load(context.Background()); err != config.ErrLicenseNotFound {
		t.Fatalf("deactivate retained local license: %v", err)
	}
}
