package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/croutoncreations/redline/mobile/core"
)

// The desktop leg is the piece that makes the relay useful: it holds the Noise
// responder, decrypts a frame, replays it against the local API, and seals the
// response back. These tests drive it without a network so the logic is pinned
// independently of the transport.

func TestDesktopServesATunnelledRequest(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dashboard" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"providers":[]}`))
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	handler := NewSessionHandler(keypair, NewForwarder(local.URL, local.Client()))

	// A phone completes the handshake.
	phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	first, err := phone.StartHandshake()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	reply, err := handler.HandleFrame(context.Background(), first)
	if err != nil {
		t.Fatalf("handshake frame: %v", err)
	}
	if err := phone.FinishHandshake(reply); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Then sends a real request.
	request, err := EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sealed, err := phone.Seal(request)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	responseFrame, err := handler.HandleFrame(context.Background(), sealed)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	opened, err := phone.Open(responseFrame)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	resp, err := DecodeResponse(opened)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status: %d", resp.Status)
	}
	if string(resp.Body) != `{"providers":[]}` {
		t.Fatalf("body: %q", resp.Body)
	}
}

// The path hardening in the forwarder must still hold when a request arrives
// the way it really will: sealed inside a Noise frame from a paired phone.
// Testing the forwarder alone would not prove the handler routes through it.
func TestHostilePathsAreRefusedThroughTheFullTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a hostile path reached the local API: %q", r.URL.RequestURI())
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	handler := NewSessionHandler(keypair, NewForwarder(local.URL, local.Client()))

	phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	first, err := phone.StartHandshake()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	reply, err := handler.HandleFrame(context.Background(), first)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := phone.FinishHandshake(reply); err != nil {
		t.Fatalf("finish: %v", err)
	}

	for _, path := range []string{
		"http://169.254.169.254/latest/meta-data",
		"https://example.com/steal",
		"//example.com/x",
		"/../../../etc/passwd",
		"/v1/%2F%2Fexample.com",
	} {
		request, err := EncodeRequestParts(http.MethodGet, path, nil, nil)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		sealed, err := phone.Seal(request)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		frame, err := handler.HandleFrame(context.Background(), sealed)
		if err != nil {
			continue // refusing the frame outright is acceptable
		}
		opened, err := phone.Open(frame)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		resp, err := DecodeResponse(opened)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Status < 400 {
			t.Fatalf("hostile path %q got status %d through the tunnel", path, resp.Status)
		}
	}
}

// A local API that is down must not take the session with it: the phone should
// see a legible error and still be able to make the next request. A dropped
// session for a transient local failure would look like a broken relay.
func TestALocalFailureDoesNotEndTheSession(t *testing.T) {
	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	// Port 1 is closed, so every forward fails.
	handler := NewSessionHandler(keypair, NewForwarder("http://127.0.0.1:1", http.DefaultClient))

	phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	first, _ := phone.StartHandshake()
	reply, err := handler.HandleFrame(context.Background(), first)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if err := phone.FinishHandshake(reply); err != nil {
		t.Fatalf("finish: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request, _ := EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
		sealed, err := phone.Seal(request)
		if err != nil {
			t.Fatalf("attempt %d seal: %v", attempt, err)
		}
		frame, err := handler.HandleFrame(context.Background(), sealed)
		if err != nil {
			t.Fatalf("attempt %d: a local failure ended the session: %v", attempt, err)
		}
		opened, err := phone.Open(frame)
		if err != nil {
			t.Fatalf("attempt %d open: %v", attempt, err)
		}
		resp, err := DecodeResponse(opened)
		if err != nil {
			t.Fatalf("attempt %d decode: %v", attempt, err)
		}
		if resp.Status != http.StatusBadGateway {
			t.Fatalf("attempt %d: expected 502 for a dead local API, got %d", attempt, resp.Status)
		}
	}
}

func TestOversizedResponseBecomesSealed502WithoutAdvancingNoiseTwice(t *testing.T) {
	keypair, _ := core.NewDesktopKeypair()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/huge-header" {
			w.Header().Set("X-Huge", strings.Repeat("h", maxTunnelPayload))
			_, _ = w.Write(make([]byte, maxTunnelBody))
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("still synchronized"))
	}))
	defer local.Close()

	handler := NewSessionHandler(keypair, NewForwarder(local.URL, local.Client()))
	phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	opening, _ := phone.StartHandshake()
	reply, err := handler.HandleFrame(context.Background(), opening)
	if err != nil {
		t.Fatal(err)
	}
	if err := phone.FinishHandshake(reply); err != nil {
		t.Fatal(err)
	}

	request, _ := EncodeRequestParts(http.MethodGet, "/huge-header", nil, nil)
	sealed, _ := phone.Seal(request)
	response, err := handler.HandleFrame(context.Background(), sealed)
	if err != nil {
		t.Fatalf("oversized local response should become a sealed 502: %v", err)
	}
	if len(response) > maxTunnelPayload {
		t.Fatalf("sealed fallback is %d bytes, limit %d", len(response), maxTunnelPayload)
	}
	opened, err := phone.Open(response)
	if err != nil {
		t.Fatalf("open fallback: %v", err)
	}
	decoded, err := DecodeResponse(opened)
	if err != nil || decoded.Status != http.StatusBadGateway || len(decoded.Header) != 0 || len(decoded.Body) != 0 {
		t.Fatalf("fallback=%#v err=%v", decoded, err)
	}

	// If the oversized response was sealed and then replaced, the sender nonce
	// advanced twice and this second request cannot decrypt. A successful normal
	// response proves the size check happened before sealing.
	request, _ = EncodeRequestParts(http.MethodGet, "/ok", nil, nil)
	sealed, _ = phone.Seal(request)
	response, err = handler.HandleFrame(context.Background(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	opened, err = phone.Open(response)
	if err != nil {
		t.Fatalf("Noise state desynchronized after 502 fallback: %v", err)
	}
	decoded, err = DecodeResponse(opened)
	if err != nil || decoded.Status != http.StatusOK || string(decoded.Body) != "still synchronized" {
		t.Fatalf("next response=%#v err=%v", decoded, err)
	}
}

// A relay is untrusted, so the first thing it might do is send garbage. That
// must end the session rather than crash the desktop service.
func TestGarbageFrameEndsTheSessionWithoutPanicking(t *testing.T) {
	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	handler := NewSessionHandler(keypair, NewForwarder("http://127.0.0.1:1", http.DefaultClient))

	for _, junk := range [][]byte{nil, {}, []byte("not a handshake"), make([]byte, 5000)} {
		if _, err := handler.HandleFrame(context.Background(), junk); err == nil {
			t.Fatalf("garbage frame %d bytes was accepted", len(junk))
		}
	}
}

// Application frames before the handshake completes must be refused: acting on
// one would mean acting on unauthenticated input.
func TestRequestsBeforeTheHandshakeAreRefused(t *testing.T) {
	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the local API was reached before a handshake completed")
	}))
	defer local.Close()

	handler := NewSessionHandler(keypair, NewForwarder(local.URL, local.Client()))
	request, _ := EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
	if _, err := handler.HandleFrame(context.Background(), request); err == nil {
		t.Fatal("a request was served before the handshake completed")
	}
}

// A second handshake on one channel must not reset its authenticated cipher
// state. Returning phones receive a fresh relay channel; accepting a reset in
// place would let an invalid frame rewrite established per-channel state.
func TestSessionHandlerRefusesASecondHandshake(t *testing.T) {
	keypair, _ := core.NewDesktopKeypair()
	handler := NewSessionHandler(keypair, NewForwarder("http://127.0.0.1:1", http.DefaultClient))

	phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	first, _ := phone.StartHandshake()
	if _, err := handler.HandleFrame(context.Background(), first); err != nil {
		t.Fatalf("first handshake: %v", err)
	}

	other, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	second, _ := other.StartHandshake()
	if _, err := handler.HandleFrame(context.Background(), second); err == nil {
		t.Fatal("a second handshake reset an established channel")
	}
}

// Without the credential a handshake is still refused, which is what makes the
// takeover above safe rather than an open door.
func TestAHandshakeWithTheWrongKeyIsStillRefused(t *testing.T) {
	keypair, _ := core.NewDesktopKeypair()
	handler := NewSessionHandler(keypair, NewForwarder("http://127.0.0.1:1", http.DefaultClient))

	phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	first, _ := phone.StartHandshake()
	if _, err := handler.HandleFrame(context.Background(), first); err != nil {
		t.Fatalf("first handshake: %v", err)
	}

	// An impostor that does not hold this desktop's key.
	impostorKeypair, _ := core.NewDesktopKeypair()
	impostor, _ := core.NewInitiatorSession(core.DesktopPublicKey(impostorKeypair))
	forged, _ := impostor.StartHandshake()
	if _, err := handler.HandleFrame(context.Background(), forged); err == nil {
		t.Fatal("a handshake encrypted to a different desktop key was accepted")
	}
}

// The keypair is the desktop's identity: every paired phone trusts it. It must
// survive a restart, or every phone would need re-pairing on every launch.
func TestKeypairPersistsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/relay-key.json"

	first, err := LoadOrCreateKeypair(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := LoadOrCreateKeypair(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if core.DesktopPublicKey(first) != core.DesktopPublicKey(second) {
		t.Fatal("the desktop identity changed across a restart")
	}
}

// A private key readable by other users on a shared machine is a key that has
// leaked.
func TestKeypairFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/relay-key.json"
	if _, err := LoadOrCreateKeypair(path); err != nil {
		t.Fatalf("create: %v", err)
	}

	mode, err := fileMode(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode&0o077 != 0 {
		t.Fatalf("keypair file mode %#o allows access beyond the owner", mode)
	}
}

// A corrupt key file should be a clear error, not a silent new identity that
// quietly unpairs every phone.
func TestCorruptKeypairFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/relay-key.json"
	if err := writeFileForTest(path, "this is not a keypair"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadOrCreateKeypair(path); err == nil {
		t.Fatal("a corrupt keypair file was silently replaced")
	}
}

// The idle timeout has to actually be wired into the session, not merely
// defined.
func TestSessionHandlerTracksIdleTime(t *testing.T) {
	keypair, _ := core.NewDesktopKeypair()
	handler := NewSessionHandler(keypair, NewForwarder("http://127.0.0.1:1", http.DefaultClient))

	start := time.Now()
	if handler.IdleSince(start).IsZero() {
		t.Fatal("a handler must track when it last saw traffic")
	}

	phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	first, _ := phone.StartHandshake()
	later := start.Add(time.Minute)
	handler.HandleFrameAt(context.Background(), first, later)
	if !handler.IdleSince(start).Equal(later) {
		t.Fatal("handling a frame must reset the idle clock")
	}
}
