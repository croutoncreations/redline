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

// The QR now carries an entitlement, and the parser has to surface it or the
// phone stores an empty one and every relayed session is refused with 402.
func TestParsePairingURLCarriesTheEntitlement(t *testing.T) {
	code := "https://desk.example.ts.net/pair#pairing_token=tok&relay=https%3A%2F%2Frelay.example.com" +
		"&key=ZGVza3RvcC1rZXk%3D&session=session-abcdefghij0123&entitlement=ent.token"

	encoded, err := ParsePairingURL(code)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var got struct {
		EntitlementToken string `json:"entitlement_token"`
	}
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EntitlementToken != "ent.token" {
		t.Errorf("entitlement_token = %q, want %q", got.EntitlementToken, "ent.token")
	}
}

// The relay refused every phone with "malformed entitlement" while the very
// same token, presented from a desktop process, was accepted.
//
// The signature half of an entitlement token is standard base64, which uses
// '+'. The QR carries it in a URL fragment, and url.ParseQuery decodes '+' as
// a space -- the same hazard the desktop key had, and which the parser already
// undoes for the key alone. The entitlement went through untouched, so one in
// every ~64 signature bytes arrived as a space, base64 decoding failed on the
// relay, and the phone heard only "requires a current subscription".
//
// Any token whose signature happens to contain no '+' pairs fine, which is why
// this survived: it depends on the bytes of one particular signature.
func TestParsePairingURLPreservesPlusInTheEntitlement(t *testing.T) {
	const entitlement = "eyJleHAiOjF9.T1jh+dnP/igZ0kpV+oQ=="
	raw := "https://desk.example:8443/pair#token=abc" +
		"&relay=https%3A%2F%2Frelay.example" +
		"&key=" + strings.ReplaceAll("ds+l3Fu+I5pT/wmwTna7cMnK+P4LZulXpQz7f+9v5+E=", "+", "%2B") +
		"&session=s" +
		"&entitlement=" + strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D").Replace(entitlement)

	out, err := ParsePairingURL(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatal(err)
	}
	if req.EntitlementToken != entitlement {
		t.Errorf("entitlement arrived as %q, want %q", req.EntitlementToken, entitlement)
	}
}

// The desktop key already had this protection; keep it honest in the same test
// file so the two cannot drift apart again.
func TestParsePairingURLRecoversPlusWhenTheQRDidNotEncodeIt(t *testing.T) {
	// A QR built by simple concatenation rather than url.Values.
	const key = "ds+l3Fu+I5pTwmwTna7cMnK+P4LZulXpQz7f+9v5+E="
	const entitlement = "eyJleHAiOjF9.T1jh+dnP"
	raw := "https://desk.example:8443/pair#token=abc&key=" + key + "&entitlement=" + entitlement

	out, err := ParsePairingURL(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	json.Unmarshal([]byte(out), &req)
	if req.DesktopKey != key {
		t.Errorf("key arrived as %q", req.DesktopKey)
	}
	if req.EntitlementToken != entitlement {
		t.Errorf("entitlement arrived as %q, want %q", req.EntitlementToken, entitlement)
	}
}
