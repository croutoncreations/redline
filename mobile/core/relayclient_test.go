package core

import (
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
		"",
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
		"",
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
		"",
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
		"",
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
		"",
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
		if _, err := DialRelay(tc.url, tc.session, tc.key, ""); err == nil {
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
		"",
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
