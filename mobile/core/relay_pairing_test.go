package core

// Tests for the three relay-pairing features implemented together:
//
//  1. ParsePairingURL accepts relay-only codes (no tailnet host).
//  2. Client with an empty baseURL routes straight to the relay.
//  3. RedeemPairing can fall back through the relay, preserving Set-Cookie.
//
// Each section follows TDD: the test is written first, the implementation
// after. The failing output for each section is in the commit message and
// the subagent report.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── Section 1: ParsePairingURL relay-only codes ────────────────────────────

// A relay-only code has the shape https://relay/pair#... where the literal
// host "relay" is the sentinel meaning "no direct endpoint". It must carry
// relay + key + session and may carry an entitlement.
//
// The phone stores an empty base_url so Client knows to skip the direct
// attempt rather than timing out waiting for a tailnet host that was never
// in the config.
func TestParsePairingURLRelayOnly(t *testing.T) {
	raw := "https://relay/pair#token=pairtoken" +
		"&relay=https%3A%2F%2Frelay.example.com" +
		"&key=ZGVza3RvcC1rZXktMzJieXRlcy1wbGFjZWhvbGRlcg%3D%3D" +
		"&session=session-abcdefghij0123" +
		"&entitlement=ent.sig"

	encoded, err := ParsePairingURL(raw)
	if err != nil {
		t.Fatalf("relay-only code rejected: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(encoded), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.BaseURL != "" {
		t.Errorf("base_url must be empty for a relay-only code, got %q", req.BaseURL)
	}
	if req.RelayURL == "" {
		t.Error("relay_url must be populated")
	}
	if req.PairingToken != "pairtoken" {
		t.Errorf("pairing_token = %q", req.PairingToken)
	}
}

// A code with a real host still works exactly as before.
func TestParsePairingURLWithHostStillWorks(t *testing.T) {
	raw := "https://macbook.example.ts.net:8443/pair#token=tok"
	encoded, err := ParsePairingURL(raw)
	if err != nil {
		t.Fatalf("host code rejected: %v", err)
	}
	var req PairingRequest
	json.Unmarshal([]byte(encoded), &req)
	if req.BaseURL != "https://macbook.example.ts.net:8443" {
		t.Errorf("base_url = %q", req.BaseURL)
	}
}

// A code with the sentinel host "relay" but no relay fields is a malformed
// code — it claims relay-only mode but gives the phone nowhere to relay to.
func TestParsePairingURLRelayHostWithoutRelayFieldsIsRejected(t *testing.T) {
	raw := "https://relay/pair#token=tok"
	_, err := ParsePairingURL(raw)
	if err == nil {
		t.Fatal("expected rejection: relay-only code with no relay fields")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "relay") {
		t.Errorf("error should mention relay, got: %v", err)
	}
}

// ── Section 2: Client with empty baseURL routes straight to relay ──────────

// A Client with an empty baseURL and a relay installed goes straight to the
// relay — no HTTP attempt at all. Verified by a test server that fails the
// test if it receives any request.
func TestClientEmptyBaseURLSkipsDirect(t *testing.T) {
	directHit := false
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHit = true
		t.Error("direct server was hit: Client with empty baseURL must not attempt direct")
		w.WriteHeader(http.StatusOK)
	}))
	defer direct.Close()
	// Client does NOT point at the direct server — baseURL is "".
	_ = direct // the server is running but the client won't know its address

	var calls int32
	fallback := RelayFallbackWithStatus(func(method, path, body string) (int, string, error) {
		atomic.AddInt32(&calls, 1)
		return 200, `{"providers":[],"health":{"scheduler_enabled":false}}`, nil
	})

	client := NewClient("", "token")
	client.SetRelayFallback(fallback)

	start := time.Now()
	_, err := client.FetchUsage()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if directHit {
		t.Error("direct server was contacted")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("relay fallback called %d times, want 1", n)
	}
	// No dial timeout should fire — if it took > 2s the client tried direct.
	if elapsed > 2*time.Second {
		t.Errorf("took %v: looks like a direct dial timeout fired", elapsed)
	}
}

// A Client with no baseURL and no relay must fail with a clear message.
func TestClientNoBaseURLNoRelayFails(t *testing.T) {
	client := NewClient("", "token")
	_, err := client.FetchUsage()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no direct endpoint") {
		t.Errorf("error should say 'no direct endpoint', got: %v", err)
	}
}

// ── Section 3: RedeemPairing via relay (Set-Cookie survives the tunnel) ───

// The relay carries response headers in TunnelResponse.Header. The existing
// Answer() encoding collapses the response to "<status> <body>" and drops
// them. Redeeming over the relay must read the Set-Cookie from the full
// response, so we need a richer fallback contract.
//
// Design chosen: RelayFallbackFull, a superset interface that returns a JSON
// envelope {"status":N,"header":{"Set-Cookie":["..."]},"body":"..."}. The
// client prefers it when present; every other endpoint keeps the compact path.
// RedeemPairingVia accepts the fallback and reads the cookie from the header.

// A redeem via a relay fallback that carries Set-Cookie must return the cookie.
func TestRedeemPairingViaRelayReturnsCookie(t *testing.T) {
	fallback := &fullFallback{
		status: 204,
		header: http.Header{"Set-Cookie": {
			"redline_api_session=relay-token; Path=/",
		}},
		body: "",
	}

	token, err := RedeemPairingVia("", "pairtoken", fallback)
	if err != nil {
		t.Fatalf("RedeemPairingVia: %v", err)
	}
	if token != "relay-token" {
		t.Errorf("token = %q, want %q", token, "relay-token")
	}
}

// A relay fallback that returns no Set-Cookie must fail the same way the
// direct path does: the app must not store an empty credential.
func TestRedeemPairingViaRelayNoCookieFails(t *testing.T) {
	fallback := &fullFallback{
		status: 204,
		header: http.Header{},
		body:   "",
	}

	_, err := RedeemPairingVia("", "pairtoken", fallback)
	if err == nil {
		t.Fatal("expected error when no cookie is returned")
	}
	if !strings.Contains(err.Error(), "no credential") {
		t.Errorf("expected 'no credential' in error, got: %v", err)
	}
}

// A direct redeem (baseURL reachable) is unchanged by this feature.
func TestRedeemPairingViaDirectStillWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "redline_api_session", Value: "direct-token"})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	token, err := RedeemPairingVia(server.URL, "pairtoken", nil)
	if err != nil {
		t.Fatalf("RedeemPairingVia direct: %v", err)
	}
	if token != "direct-token" {
		t.Errorf("token = %q, want %q", token, "direct-token")
	}
}

// fullFallback implements RelayFallbackFull for tests.
type fullFallback struct {
	status int
	header http.Header
	body   string
}

func (f *fullFallback) Do(method, path, body string) (string, error) {
	return FormatRelayAnswer(f.status, f.body), nil
}

func (f *fullFallback) DoFull(method, path, body string) (string, error) {
	resp := relayFullResponse{
		Status: f.status,
		Header: f.header,
		Body:   []byte(f.body),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// The full envelope carries the desktop's headers so the pairing cookie can
// cross the relay. It must not carry the ones that describe a wire the body
// no longer travelled on: the body was re-serialised into JSON, so the
// desktop's Content-Length is wrong, and Transfer-Encoding and Connection
// describe a hop that does not exist here. Nothing reads them today; the
// point is that nothing ever can.
func TestRelayFullResponseKeepsOnlyTheHeadersThatStillMeanSomething(t *testing.T) {
	fallback := &fullFallback{
		status: 204,
		header: http.Header{
			"Set-Cookie":        {"redline_api_session=cred; Path=/"},
			"Content-Type":      {"application/json"},
			"Content-Length":    {"9999"},
			"Transfer-Encoding": {"chunked"},
			"Connection":        {"keep-alive"},
			"X-Anything":        {"else"},
		},
	}
	client := NewClient("", "")
	client.SetRelayFallback(fallback)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := client.doCapturingResponse(ctx, http.MethodPost, "/v1/pairing/redeem", map[string]string{"pairing_token": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Set-Cookie"); !strings.Contains(got, "redline_api_session=cred") {
		t.Errorf("the cookie is the whole point and was dropped: %q", got)
	}
	if response.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type should survive: %q", response.Header.Get("Content-Type"))
	}
	for _, dropped := range []string{"Content-Length", "Transfer-Encoding", "Connection", "X-Anything"} {
		if v := response.Header.Get(dropped); v != "" {
			t.Errorf("%s = %q should not have crossed the relay", dropped, v)
		}
	}
}

// A fallback that reaches RedeemPairingVia across gomobile arrives as a
// proxy for the DECLARED parameter type and nothing more. If that type is
// RelayFallback, a type assertion to RelayFallbackFull fails on every real
// phone -- while passing in every Go test, where the fake satisfies both. The
// cookie is silently dropped and pairing reports "accepted but returned no
// credential". Seen on a Pixel, from the desktop app's pairing sheet.
//
// So the parameter must be RelayFallbackFull itself. This test hands in a
// value that is ONLY a RelayFallbackFull through the narrowest possible
// static type and checks the cookie still arrives -- and, since Go cannot
// express "proxy that hides methods", it also pins the signature by
// assignment, so narrowing it back to RelayFallback is a compile error.
func TestRedeemPairingViaDemandsTheFullFallbackByType(t *testing.T) {
	var redeem func(string, string, RelayFallbackFull) (string, error) = RedeemPairingVia
	fallback := &fullFallback{
		status: 204,
		header: http.Header{"Set-Cookie": {"redline_api_session=cred; Path=/"}},
	}
	cred, err := redeem("https://10.255.255.1:9", "tok", fallback)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if cred != "cred" {
		t.Errorf("credential = %q", cred)
	}
}
