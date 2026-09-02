package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsePairingURL(t *testing.T) {
	raw := "https://macbook.example.ts.net:8443/pair#pairing_token=one-time-token"

	encoded, err := ParsePairingURL(raw)
	if err != nil {
		t.Fatalf("ParsePairingURL: %v", err)
	}
	var pairing PairingRequest
	if err := json.Unmarshal([]byte(encoded), &pairing); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if pairing.BaseURL != "https://macbook.example.ts.net:8443" {
		t.Errorf("base url = %q", pairing.BaseURL)
	}
	if pairing.PairingToken != "one-time-token" {
		t.Errorf("token = %q", pairing.PairingToken)
	}
}

// The default HTTPS port is left off the base URL, so the app does not display
// or store a redundant :443.
func TestParsePairingURLOmitsDefaultPort(t *testing.T) {
	encoded, err := ParsePairingURL("https://macbook.example.ts.net/pair#pairing_token=abc")
	if err != nil {
		t.Fatalf("ParsePairingURL: %v", err)
	}
	var pairing PairingRequest
	_ = json.Unmarshal([]byte(encoded), &pairing)
	if pairing.BaseURL != "https://macbook.example.ts.net" {
		t.Errorf("base url = %q, want no explicit port", pairing.BaseURL)
	}
}

// A QR from some other application must be rejected clearly rather than
// producing a client pointed at nothing.
func TestParsePairingURLRejectsForeignCodes(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/pair",                         // no token
		"https://example.com/login#pairing_token=abc",      // wrong path
		"not a url at all",                                 // not a URL
		"ftp://example.com/pair#pairing_token=abc",         // wrong scheme
		"http://insecure.example.com/pair#pairing_token=a", // plain HTTP to a remote host
	} {
		if _, err := ParsePairingURL(raw); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

// Pairing against a desktop on the same machine is how the app is developed and
// how a loopback tunnel works, so plain HTTP to loopback stays allowed.
func TestParsePairingURLAllowsLoopbackOverHTTP(t *testing.T) {
	if _, err := ParsePairingURL("http://127.0.0.1:7436/pair#pairing_token=abc"); err != nil {
		t.Errorf("loopback pairing must be allowed: %v", err)
	}
}

// Redeeming exchanges the one-time token for the durable credential the app
// stores. The service returns it as a session cookie.
func TestRedeemPairingReturnsTheAPIToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/pairing/redeem" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Token string `json:"pairing_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Token != "one-time-token" {
			t.Errorf("pairing token = %q", body.Token)
		}
		http.SetCookie(w, &http.Cookie{Name: "redline_api_session", Value: "the-durable-api-token"})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	token, err := RedeemPairing(server.URL, "one-time-token")
	if err != nil {
		t.Fatalf("RedeemPairing: %v", err)
	}
	if token != "the-durable-api-token" {
		t.Errorf("token = %q", token)
	}
}

// An expired or already-used pairing token must say so, because the fix is to
// generate a fresh QR rather than to retry.
func TestRedeemPairingReportsAnExpiredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid or expired Redline pairing token"}`))
	}))
	defer server.Close()

	_, err := RedeemPairing(server.URL, "stale")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the service's explanation should survive, got %v", err)
	}
}

// A redeem that succeeds but carries no cookie leaves the app with no
// credential; failing loudly beats storing an empty token and looking
// unauthorized later for no visible reason.
func TestRedeemPairingFailsWithoutACredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if _, err := RedeemPairing(server.URL, "token"); err == nil {
		t.Fatal("a redeem without a credential must be an error")
	}
}
