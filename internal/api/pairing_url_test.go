package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/store"
)

// The service composes the pairing QR itself, so every surface that shows one
// -- the CLI, the menu bar, later iOS -- renders the same code.
//
// The menu-bar app built its own URL from the trusted host and the token, and
// nothing else. When the QR grew relay fields the CLI got them and the sheet
// did not, so every phone paired from the desktop app had no relay and no way
// to know. Two builders of one format is one too many; the service is the
// place that already holds the config, the identity and the token.

func pairingHandler(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return api.NewServer(cfg, db, func() time.Time { return apiNow })
}

func createPairing(t *testing.T, handler http.Handler, cfg config.Config, query string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	// Over loopback, the way the menu-bar app reaches the service. A desktop
	// with no trusted host at all can still mint a code this way, which is the
	// relay-only user's whole situation.
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/pairing"+query, nil)
	request.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	request.RemoteAddr = "127.0.0.1:54321"
	handler.ServeHTTP(recorder, request)
	var body map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
		}
	}
	return recorder.Code, body
}

func fragmentOf(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(parsed.EscapedFragment())
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestPairingResponseCarriesTheURLForATailnetOnlyDesktop(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	handler := pairingHandler(t, cfg)

	code, body := createPairing(t, handler, cfg, "")
	if code != http.StatusCreated {
		t.Fatalf("status = %d body = %v", code, body)
	}
	pairingURL, _ := body["pairing_url"].(string)
	if !strings.HasPrefix(pairingURL, "https://macbook.example.ts.net:8443/pair#") {
		t.Fatalf("pairing_url = %q", pairingURL)
	}
	fragment := fragmentOf(t, pairingURL)
	if fragment.Get("pairing_token") != body["pairing_token"] {
		t.Errorf("the URL must carry the same token the response does")
	}
	if fragment.Has("relay") {
		t.Errorf("no relay is configured, so none may be published")
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 1 || routes[0] != "direct" {
		t.Errorf("routes = %v, want [direct]", routes)
	}
}

func TestPairingResponseCarriesTheRelayWhenEnabled(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.Database = filepath.Join(t.TempDir(), "redline.db")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	cfg.Relay = config.Relay{
		Enabled:          true,
		URL:              "https://relay.example",
		SessionID:        "session-0123456789abcdefghijklmnop",
		EntitlementToken: "eyJleHAiOjF9.sig+with/plus==",
	}
	handler := pairingHandler(t, cfg)

	code, body := createPairing(t, handler, cfg, "")
	if code != http.StatusCreated {
		t.Fatalf("status = %d body = %v", code, body)
	}
	fragment := fragmentOf(t, body["pairing_url"].(string))
	if fragment.Get("relay") != "https://relay.example" {
		t.Errorf("relay = %q", fragment.Get("relay"))
	}
	if fragment.Get("session") != cfg.Relay.SessionID {
		t.Errorf("session = %q", fragment.Get("session"))
	}
	// The '+' in the entitlement is the byte that was silently lost once; the
	// URL the service hands out must round-trip it.
	if fragment.Get("entitlement") != cfg.Relay.EntitlementToken {
		t.Errorf("entitlement = %q, want %q", fragment.Get("entitlement"), cfg.Relay.EntitlementToken)
	}
	if key := fragment.Get("key"); len(key) != 44 {
		t.Errorf("key should be a base64 32-byte public key, got %q", key)
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 2 || routes[0] != "direct" || routes[1] != "relay" {
		t.Errorf("routes = %v, want [direct relay]", routes)
	}
}

// A desktop with no trusted host and a relay is the whole point: a user who
// never set up Tailscale.
func TestPairingResponseIsRelayOnlyWithoutATrustedHost(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.Database = filepath.Join(t.TempDir(), "redline.db")
	cfg.API.TrustedHosts = nil
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	cfg.Relay = config.Relay{
		Enabled:   true,
		URL:       "https://relay.example",
		SessionID: "session-0123456789abcdefghijklmnop",
	}
	handler := pairingHandler(t, cfg)

	code, body := createPairing(t, handler, cfg, "")
	if code != http.StatusCreated {
		t.Fatalf("status = %d body = %v", code, body)
	}
	pairingURL := body["pairing_url"].(string)
	if !strings.HasPrefix(pairingURL, "https://relay/pair#") {
		t.Errorf("a relay-only code uses the sentinel host, got %q", pairingURL)
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 1 || routes[0] != "relay" {
		t.Errorf("routes = %v, want [relay]", routes)
	}
}

// ?relay_only=1 lets the desktop app offer a "pair over the relay only" choice
// for a user who is on the tailnet now but wants to test the path they will
// use away from home.
func TestPairingResponseHonoursRelayOnly(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.Database = filepath.Join(t.TempDir(), "redline.db")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	cfg.Relay = config.Relay{Enabled: true, URL: "https://relay.example", SessionID: "session-0123456789abcdefghijklmnop"}
	handler := pairingHandler(t, cfg)

	_, body := createPairing(t, handler, cfg, "?relay_only=1")
	if !strings.HasPrefix(body["pairing_url"].(string), "https://relay/pair#") {
		t.Errorf("relay_only was ignored: %q", body["pairing_url"])
	}
}

// With neither a trusted host nor a relay there is nowhere to send a phone.
// The token is still minted -- the web /pair page redeems it from a browser on
// this machine -- but no URL is offered, and the caller is told why.
func TestPairingResponseOmitsTheURLWhenNoRouteExists(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = nil
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	handler := pairingHandler(t, cfg)

	code, body := createPairing(t, handler, cfg, "")
	if code != http.StatusCreated {
		t.Fatalf("status = %d body = %v", code, body)
	}
	if _, present := body["pairing_url"]; present {
		t.Errorf("no route means no URL, got %v", body["pairing_url"])
	}
	if body["pairing_token"] == "" {
		t.Errorf("the token itself is still useful to the web pair page")
	}
	routes, _ := body["routes"].([]any)
	if len(routes) != 0 {
		t.Errorf("routes = %v, want none", routes)
	}
}

// A trusted host may now carry a port, and requests must still match it.
//
// allowedHost stripped the port from the incoming Host header and compared
// the bare name against the configured entry verbatim -- so the moment a
// port went into the config, every request from the tailnet was refused as
// untrusted. Config validation and request matching have to agree on what an
// entry means.
func TestTrustedHostWithAPortStillAdmitsRequests(t *testing.T) {
	cfg := testConfig("http://unused")
	cfg.API.TrustedHosts = []string{"macbook.example.ts.net:8443"}
	cfg.APIToken = "test-token-that-is-at-least-thirty-two-characters"
	handler := pairingHandler(t, cfg)

	for _, hostHeader := range []string{"macbook.example.ts.net:8443", "macbook.example.ts.net"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "https://"+hostHeader+"/v1/dashboard", nil)
		request.Header.Set("Authorization", "Bearer "+cfg.APIToken)
		request.Header.Set("X-Forwarded-Proto", "https")
		request.RemoteAddr = "100.101.102.103:54321"
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusForbidden {
			t.Errorf("Host %q was refused against trusted entry with a port: %s", hostHeader, recorder.Body.String())
		}
	}
}
