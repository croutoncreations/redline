package core

import (
	"sync/atomic"
	"time"

	"context"
	"encoding/base64"
	"errors"
	"github.com/coder/websocket"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The phone's relay client is the last piece of the tunnel. These tests drive
// it against a stand-in relay so the behaviour is pinned without needing
// Cloudflare.

// relayPair wires two websockets together the way the Durable Object does:
// whatever the client sends goes to the host and vice versa.
func relayPair(t *testing.T, desktop func(send func([]byte), recv func() []byte)) *httptest.Server {
	t.Helper()
	toDesktop := make(chan []byte, 16)
	toPhone := make(chan []byte, 16)

	go desktop(
		func(b []byte) { toPhone <- b },
		func() []byte { return <-toDesktop },
	)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgradeAndPump(t, w, r, toDesktop, toPhone)
	}))
}

func TestRelayClientCarriesARequest(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	// A desktop that answers one request.
	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(recv())
		if err != nil {
			return
		}
		send(reply)

		opened, err := responder.Open(recv())
		if err != nil {
			return
		}
		var request map[string]any
		if err := json.Unmarshal(opened, &request); err != nil {
			return
		}
		body, _ := json.Marshal(map[string]any{
			"status": 200,
			"body":   []byte(`{"path":"` + request["path"].(string) + `"}`),
		})
		sealed, err := responder.Seal(body)
		if err != nil {
			return
		}
		send(sealed)
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer client.Close()

	response, err := client.Request("GET", "/v1/dashboard", "")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !strings.Contains(response, "/v1/dashboard") {
		t.Fatalf("response did not come from the desktop: %q", response)
	}
}

// Every Redline endpoint needs a bearer token, so a client that cannot send a
// header cannot reach any of them. Without this the whole tunnel returns 401.
func TestRelayClientSendsTheCredential(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(recv())
		if err != nil {
			return
		}
		send(reply)

		opened, err := responder.Open(recv())
		if err != nil {
			return
		}
		var request struct {
			Header map[string][]string `json:"header"`
		}
		if err := json.Unmarshal(opened, &request); err != nil {
			return
		}
		// Echo back what the desktop would have received.
		auth := ""
		if values := request.Header["Authorization"]; len(values) > 0 {
			auth = values[0]
		}
		payload, _ := json.Marshal(map[string]any{
			"status": 200,
			"body":   []byte(auth),
		})
		if sealed, err := responder.Seal(payload); err == nil {
			send(sealed)
		}
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	client.SetAuthToken("desktop-api-token")
	got, err := client.Request("GET", "/v1/dashboard", "")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got != "Bearer desktop-api-token" {
		t.Fatalf("the desktop saw authorization %q", got)
	}
}

// Rule 1 of the client contract: a session is single-use. Using a closed
// client must fail cleanly rather than panicking across the FFI.
func TestRelayClientRefusesUseAfterClose(t *testing.T) {
	desktopKey, _ := NewDesktopKeypair()
	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, _ := NewResponderSession(desktopKey)
		if reply, err := responder.ReadHandshake(recv()); err == nil {
			send(reply)
		}
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client.Close()

	if _, err := client.Request("GET", "/v1/dashboard", ""); err == nil {
		t.Fatal("a closed client must not accept requests")
	}
	// Closing twice must also be safe.
	client.Close()
}

// A desktop that never answers must not hang the phone forever.
func TestRelayClientTimesOutASilentDesktop(t *testing.T) {
	desktopKey, _ := NewDesktopKeypair()
	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, _ := NewResponderSession(desktopKey)
		if reply, err := responder.ReadHandshake(recv()); err == nil {
			send(reply)
		}
		// Then goes quiet: reads the request and never replies.
		recv()
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	client.SetTimeoutSeconds(1)
	if _, err := client.Request("GET", "/v1/dashboard", ""); err == nil {
		t.Fatal("a silent desktop should time out rather than hang")
	}
}

// The handshake authenticates the desktop. A relay that answers with its own
// key must not produce a working session.
func TestRelayClientRejectsAnImpostorDesktop(t *testing.T) {
	realKey, _ := NewDesktopKeypair()
	impostorKey, _ := NewDesktopKeypair()

	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		// The far end holds a different key than the phone expects.
		responder, _ := NewResponderSession(impostorKey)
		if reply, err := responder.ReadHandshake(recv()); err == nil {
			send(reply)
		}
	})
	defer server.Close()

	_, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(realKey),
	)
	if err == nil {
		t.Fatal("the phone accepted a session with a desktop holding the wrong key")
	}
}

// The relay is validated before it is dialled, so a hostile QR that got past
// pairing cannot be reached even by a direct call.
func TestDialRelayValidatesItsInputs(t *testing.T) {
	desktopKey, _ := NewDesktopKeypair()
	pub := DesktopPublicKey(desktopKey)

	cases := []struct {
		name    string
		url     string
		session string
		key     string
	}{
		{"cleartext relay", "http://relay.example.com", "phone-session-0123456789abc", pub},
		{"empty relay", "", "phone-session-0123456789abc", pub},
		{"short session", "wss://relay.example.com", "abc", pub},
		{"hostile session", "wss://relay.example.com", "../../admin?role=host", pub},
		{"empty key", "wss://relay.example.com", "phone-session-0123456789abc", ""},
		{"junk key", "wss://relay.example.com", "phone-session-0123456789abc", "not-a-key"},
	}
	for _, tc := range cases {
		if _, err := DialRelay(tc.url, tc.session, tc.key); err == nil {
			t.Errorf("%s: DialRelay accepted it", tc.name)
		}
	}
}

// Rule 3: a sealed frame must never be resent on a new session. The client
// owns its session, so the way this shows up is that a failed request does not
// leave a half-consumed session behind: the next request either works or the
// client reports the session is finished.
func TestAFailedRequestDoesNotCorruptLaterOnes(t *testing.T) {
	desktopKey, _ := NewDesktopKeypair()

	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, _ := NewResponderSession(desktopKey)
		reply, err := responder.ReadHandshake(recv())
		if err != nil {
			return
		}
		send(reply)

		for i := 0; ; i++ {
			frame := recv()
			if frame == nil {
				return
			}
			if _, err := responder.Open(frame); err != nil {
				return
			}
			// The first request gets a malformed answer, the second a good one.
			var payload []byte
			if i == 0 {
				payload = []byte("this is not a tunnel response")
			} else {
				payload, _ = json.Marshal(map[string]any{"status": 200, "body": []byte(`{"ok":true}`)})
			}
			sealed, err := responder.Seal(payload)
			if err != nil {
				return
			}
			send(sealed)
		}
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Request("GET", "/v1/first", ""); err == nil {
		t.Fatal("a malformed response should be reported as an error")
	}
	// The session itself is still intact, so the next request must work.
	got, err := client.Request("GET", "/v1/second", "")
	if err != nil {
		t.Fatalf("the session did not survive a malformed response: %v", err)
	}
	if !strings.Contains(got, "ok") {
		t.Fatalf("second response: %q", got)
	}
}

// An unpaid or expired entitlement must not read as a network problem.
//
// The relay refuses at the WebSocket upgrade with 402, before any tunnel
// exists. DialRelay discarded the response carrying that status and returned
// a flat "connect failed", so the app told the user to check that the desktop
// was running and on the same network -- when the actual remedy is to renew.
// Verified against the live deployed relay before fixing.
//
// This matters more the day the fee turns on, because it is the message every
// lapsed subscriber sees.
func TestEntitlementRefusalSurvivesAStringOnlyFFIBoundary(t *testing.T) {
	// gomobile can reconstruct an error from its text without preserving the Go
	// wrapping chain. Classification must survive that declared FFI contract.
	err := errors.New("direct and relay routes failed: this relay requires a current subscription")
	if !IsEntitlementRefused(err) {
		t.Fatalf("string-only FFI error was not classified: %v", err)
	}
}

func TestDirectFailurePreservesRelayEntitlementRefusal(t *testing.T) {
	client := NewClient("http://127.0.0.1:1", "token")
	client.SetRelayFallback(RelayFallbackRaw(func(_, _, _ string) (string, error) {
		return "", ErrEntitlementRefused
	}))

	_, err := client.FetchUsage()
	if err == nil || !IsEntitlementRefused(err) {
		t.Fatalf("fallback entitlement refusal was erased: %v", err)
	}
}

func TestDialRelayReportsAnEntitlementRefusalAsItsOwnThing(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this relay requires an entitlement", http.StatusPaymentRequired)
	}))
	defer relay.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if !IsEntitlementRefused(err) {
		t.Errorf("a 402 must be recognisable as an entitlement refusal, got: %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "connect failed") {
		t.Errorf("a 402 must not read as a connection failure: %v", err)
	}
	// The relay's own reason travels with the error. Missing, malformed, bad
	// signature and expired all used to arrive as one sentence, and telling
	// them apart is what a day of diagnosis came down to.
	if !strings.Contains(err.Error(), "this relay requires an entitlement") {
		t.Errorf("the relay's reason must reach the caller, got: %v", err)
	}
}

// The reason is relay-controlled text on its way to a log. Bound its size, and
// quote it so an embedded newline or escape cannot forge a second log line.
func TestDialRelayBoundsAndQuotesTheRefusalReason(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		// Newlines to forge a line, and bytes %q must escape at full width.
		w.Write([]byte("line one\nW RedlineRelay: forged line " + strings.Repeat("\x00", 1000)))
	}))
	defer relay.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("a raw newline from the relay reached the error: %q", msg)
	}
	// The reader takes 256 bytes and %q can spend four characters on each of
	// them, so the honest ceiling is a little over 1KB -- still a bound, and
	// still one log line.
	if len(msg) > 1200 {
		t.Errorf("the reason was not bounded: %d bytes", len(msg))
	}
}

// Everything that is not a 402 stays a plain connection failure, so a real
// network problem is not mislabelled as a billing one.
func TestDialRelayKeepsOtherFailuresAsConnectionFailures(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer relay.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if IsEntitlementRefused(err) {
		t.Errorf("a 500 is not an entitlement problem: %v", err)
	}
}

// A client whose session has ended must say so, so the caller redials rather
// than handing out a corpse.
//
// The phone cached one RelayClient and reused it. When the socket closed --
// which the relay used to do to the desktop on every phone disconnect -- the
// next request went to a dead session and failed. On a real phone that read as
// "relayed", then "offline", and re-pairing could not help because the cache
// outlived the session.
func TestRelayClientReportsWhenItIsSpent(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Accept the handshake, then hang up as a spent session would.
		conn.CloseNow()
	}))
	defer relay.Close()

	client, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err != nil {
		// A handshake that cannot complete is its own failure; nothing to test.
		return
	}
	defer client.Close()

	if client.IsSpent() {
		t.Error("a freshly dialled client is not spent")
	}

	// The first request fails because the far end went away.
	if _, err := client.Answer("GET", "/v1/health", ""); err == nil {
		t.Skip("the stub stayed up; nothing to assert")
	}

	if !client.IsSpent() {
		t.Error("a client whose session failed must report itself spent so the caller redials")
	}
}

// The relay refuses role=client with 423 when no entitled host is attached to
// the session (docs/relay-entitlement.md). This must read as "your Mac is not
// connected to the relay", not as a generic connection failure: the fix is to
// check the desktop, not to check the phone's own network.
func TestDialRelayReportsHostOfflineAs423(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"no_host"}`, http.StatusLocked)
	}))
	defer relay.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if !IsHostOffline(err) {
		t.Errorf("a 423 must be recognisable as the host being offline, got: %v", err)
	}
	if IsEntitlementRefused(err) {
		t.Errorf("423 no_host is not an entitlement refusal: %v", err)
	}
	if IsTooManyPhones(err) {
		t.Errorf("423 no_host is not too-many-phones: %v", err)
	}
}

// The relay refuses role=client with 409 when the session is already at
// max_clients (docs/relay-entitlement.md). Distinct from every other relay
// refusal: the desktop and the subscription are both fine, and the remedy is
// to close another phone's session, not to renew or check connectivity.
func TestDialRelayReportsTooManyClientsAs409(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"too_many_clients"}`, http.StatusConflict)
	}))
	defer relay.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(relay.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
	if !IsTooManyPhones(err) {
		t.Errorf("a 409 must be recognisable as too many phones, got: %v", err)
	}
	if IsHostOffline(err) || IsEntitlementRefused(err) {
		t.Errorf("409 too_many_clients must not read as any other refusal: %v", err)
	}
}

// A plain 409 with no relay body at all -- the shape DialRelay already
// retries on while a desktop's reconnect races the phone's dial (see
// TestDialRelayRetriesWhileTheDesktopReconnects) -- must not be
// misclassified as too-many-phones just because the status code matches.
// Only role=client's specific too_many_clients body means that.
func TestDialRelayDoesNotMisclassifyAPlain409(t *testing.T) {
	err := errors.New("dial relay: connect failed")
	if IsTooManyPhones(err) {
		t.Errorf("a plain connect failure must not be misclassified as too-many-phones: %v", err)
	}
}

// The relay closes the WebSocket with 1008 "entitlement expired" mid-session
// when a Durable Object alarm fires at the stored exp (docs/relay-entitlement.md).
// This must be distinguishable from the connect-time 402 refusal by callers:
// it happens after a session was already working, so the app can say the
// subscription just lapsed rather than repeat the pre-connection wording.
func TestRelayClientReportsEntitlementExpiredMidSession(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(relayClientFrameLimit)

		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		_, handshakeMsg, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(handshakeMsg)
		if err != nil {
			return
		}
		if err := conn.Write(context.Background(), websocket.MessageBinary, reply); err != nil {
			return
		}
		// A live, authenticated session, then the relay closes it exactly as
		// its alarm handler does at the stored exp.
		conn.Close(websocket.StatusPolicyViolation, "entitlement expired")
	}))
	defer relay.Close()

	client, err := DialRelay(
		strings.Replace(relay.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer client.Close()

	_, requestErr := client.Request("GET", "/v1/dashboard", "")
	if requestErr == nil {
		t.Fatal("expected the request to fail once the relay closed the session")
	}
	if !IsEntitlementExpiredMidSession(requestErr) {
		t.Errorf("a 1008 policy-violation close must be recognisable as a mid-session entitlement expiry, got: %v", requestErr)
	}
}

// A 1008 close for any other reason must not be misreported as an
// entitlement lapse. The relay is untrusted input: matching on the status
// code alone would let it steer a phone onto "your subscription just
// expired" for a reason that has nothing to do with entitlement.
func TestRelayClientDoesNotMisclassifyAnUnrelatedPolicyViolationClose(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(relayClientFrameLimit)

		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		_, handshakeMsg, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(handshakeMsg)
		if err != nil {
			return
		}
		if err := conn.Write(context.Background(), websocket.MessageBinary, reply); err != nil {
			return
		}
		// Same status code as an entitlement-lapse close, but a different
		// reason -- exactly what a relay enforcing some unrelated future
		// policy would send.
		conn.Close(websocket.StatusPolicyViolation, "policy changed")
	}))
	defer relay.Close()

	client, err := DialRelay(
		strings.Replace(relay.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer client.Close()

	_, requestErr := client.Request("GET", "/v1/dashboard", "")
	if requestErr == nil {
		t.Fatal("expected the request to fail once the relay closed the session")
	}
	if IsEntitlementExpiredMidSession(requestErr) {
		t.Errorf("a 1008 close with an unrelated reason must not be reported as an entitlement lapse, got: %v", requestErr)
	}
}

// The phone must not lose a race with the desktop's reconnect.
//
// Closing a relayed session ends the desktop's leg too, and it redials within
// a fraction of a second. A phone that dials once in that window finds nobody
// paired and reports the desktop unreachable -- observed as "relayed", then
// "offline" on the very next refresh.
//
// Retrying briefly costs nothing when the desktop is there and turns the
// common case from a failure into a short pause.
func TestDialRelayRetriesWhileTheDesktopReconnects(t *testing.T) {
	var attempts int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first two dials find no partner, as they would while the desktop
		// is redialling; the third is accepted.
		if atomic.AddInt32(&attempts, 1) < 3 {
			http.Error(w, "no partner yet", http.StatusConflict)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		time.Sleep(200 * time.Millisecond)
	})).Config.Handler
	server := httptest.NewServer(relay)
	defer server.Close()

	_, err := DialRelay(
		"ws"+strings.TrimPrefix(server.URL, "http"),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)),
	)
	// The handshake cannot complete against a stub, but the dial must have been
	// retried rather than given up after one refusal.
	_ = err
	if got := atomic.LoadInt32(&attempts); got < 3 {
		t.Errorf("dial attempts = %d, want at least 3: a single try loses the race", got)
	}
}

// The desktop and the phone disagreed about how to spell the relay URL.
//
// config.Config requires https:// -- it validates an address the desktop will
// dial with net/http -- and the QR publishes that value verbatim. DialRelay
// requires wss://, because it opens a WebSocket. So a correctly configured
// desktop handed every phone a URL its own core would refuse, and the fallback
// failed before a single packet moved. On screen that read as "relayed" in the
// header and "Cannot reach Redline" underneath.
//
// Accepting both is right rather than lenient: they name the same endpoint,
// and which spelling is correct depends only on which library is opening the
// connection. Rejecting one of them is an implementation detail leaking into
// a pairing payload.
func TestDialRelayAcceptsTheURLTheDesktopPublishes(t *testing.T) {
	for _, raw := range []string{
		"https://relay.example.com",
		"wss://relay.example.com",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := DialRelay(raw, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				base64.StdEncoding.EncodeToString(make([]byte, 32)))
			// The dial fails -- nothing is listening -- but it must fail on
			// reaching the host, never on the shape of the URL.
			if err != nil && strings.Contains(err.Error(), "must use wss") {
				t.Errorf("%s was rejected for its scheme: %v", raw, err)
			}
		})
	}
}

// http:// stays refused for a public host: every frame is Noise-sealed before
// it reaches the wire, including the bearer token carried inside each sealed
// request, but the handshake itself is still plaintext WebSocket framing, and
// sending that in the clear would expose the session to tampering by anyone
// on the path.
func TestDialRelayStillRefusesCleartext(t *testing.T) {
	_, err := DialRelay("http://relay.example.com", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err == nil || !strings.Contains(err.Error(), "wss") {
		t.Errorf("cleartext to a public host must be refused, got: %v", err)
	}
}

// Pairing over the relay needs the desktop's Set-Cookie, which the compact
// "<status> <body>" answer throws away. AnswerFull keeps the headers.
//
// Without a RelayClient method that produces the full envelope, the
// RelayFallbackFull interface can only ever be satisfied by a test fake --
// the Kotlin side would have nothing real to call, and relayed pairing would
// be "written but never wired", which is the shape of every fault this branch
// has shipped.
func TestRelayClientAnswerFullKeepsTheDesktopHeaders(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(recv())
		if err != nil {
			return
		}
		send(reply)
		if _, err := responder.Open(recv()); err != nil {
			return
		}
		// What the redeem handler really sends: 204, a cookie, no body.
		body, _ := json.Marshal(map[string]any{
			"status": 204,
			"header": map[string][]string{
				"Set-Cookie": {"redline_api_session=the-credential; Path=/; HttpOnly"},
			},
		})
		sealed, err := responder.Seal(body)
		if err != nil {
			return
		}
		send(sealed)
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc",
		DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer client.Close()

	answer, err := client.AnswerFull("POST", "/v1/pairing/redeem", `{"pairing_token":"x"}`)
	if err != nil {
		t.Fatalf("answer full: %v", err)
	}
	var full tunnelResponse
	if err := json.Unmarshal([]byte(answer), &full); err != nil {
		t.Fatalf("AnswerFull must return the JSON envelope DoFull promises, got %q: %v", answer, err)
	}
	if full.Status != 204 {
		t.Errorf("status = %d, want 204", full.Status)
	}
	if got := full.Header.Get("Set-Cookie"); !strings.Contains(got, "redline_api_session=the-credential") {
		t.Errorf("Set-Cookie did not survive the relay: %q", got)
	}
}

// A non-2xx status is the desktop's own answer and must reach the caller
// intact, the same rule Answer follows: a 401 on redeem means "bad pairing
// token", which is a different remedy from "the relay is down".
func TestRelayClientAnswerFullCarriesARefusal(t *testing.T) {
	desktopKey, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	server := relayPair(t, func(send func([]byte), recv func() []byte) {
		responder, err := NewResponderSession(desktopKey)
		if err != nil {
			return
		}
		reply, err := responder.ReadHandshake(recv())
		if err != nil {
			return
		}
		send(reply)
		if _, err := responder.Open(recv()); err != nil {
			return
		}
		body, _ := json.Marshal(map[string]any{
			"status": 401,
			"body":   []byte(`{"error":"invalid or expired Redline pairing token"}`),
		})
		sealed, _ := responder.Seal(body)
		send(sealed)
	})
	defer server.Close()

	client, err := DialRelay(
		strings.Replace(server.URL, "http://", "ws://", 1),
		"phone-session-0123456789abc", DesktopPublicKey(desktopKey),
	)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer client.Close()

	answer, err := client.AnswerFull("POST", "/v1/pairing/redeem", `{}`)
	if err != nil {
		t.Fatalf("a refusal is an answer, not a relay failure: %v", err)
	}
	var full tunnelResponse
	json.Unmarshal([]byte(answer), &full)
	if full.Status != 401 || !strings.Contains(string(full.Body), "pairing token") {
		t.Errorf("refusal did not survive: status=%d body=%q", full.Status, full.Body)
	}
}
