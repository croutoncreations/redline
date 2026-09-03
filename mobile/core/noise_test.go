package core

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// handshake drives both halves to completion and returns the two sessions.
//
// The phone is the initiator because it is the side that reaches out; the
// desktop is waiting on the relay for whoever arrives.
func handshake(t *testing.T) (*NoiseSession, *NoiseSession) {
	t.Helper()

	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("desktop keypair: %v", err)
	}

	responder, err := NewResponderSession(desktop)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	// The phone learns the desktop's public key from the pairing QR, which is
	// what lets this be a one round trip IK handshake.
	initiator, err := NewInitiatorSession(DesktopPublicKey(desktop))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}

	first, err := initiator.StartHandshake()
	if err != nil {
		t.Fatalf("start handshake: %v", err)
	}
	reply, err := responder.ReadHandshake(first)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if err := initiator.FinishHandshake(reply); err != nil {
		t.Fatalf("finish handshake: %v", err)
	}
	return initiator, responder
}

func TestHandshakeEstablishesATwoWayChannel(t *testing.T) {
	phone, desktop := handshake(t)

	if !phone.Established() || !desktop.Established() {
		t.Fatal("both sides should report an established session")
	}

	sealed, err := phone.Seal([]byte("GET /v1/dashboard"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	opened, err := desktop.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != "GET /v1/dashboard" {
		t.Fatalf("desktop read %q", opened)
	}

	// And back the other way, on the separate cipher state.
	sealed, err = desktop.Seal([]byte("200 OK"))
	if err != nil {
		t.Fatalf("seal reply: %v", err)
	}
	opened, err = phone.Open(sealed)
	if err != nil {
		t.Fatalf("open reply: %v", err)
	}
	if string(opened) != "200 OK" {
		t.Fatalf("phone read %q", opened)
	}
}

// This is the property the entire relay design rests on: the relay forwards
// bytes it cannot read. If this test ever fails, the product claim is false.
func TestRelayCannotReadFrames(t *testing.T) {
	phone, _ := handshake(t)

	secret := "Bearer sk-ant-supersecret-token"
	sealed, err := phone.Seal([]byte(secret))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if bytes.Contains(sealed, []byte(secret)) {
		t.Fatal("the token appears in plaintext in the frame the relay forwards")
	}
	if bytes.Contains(sealed, []byte("Bearer")) {
		t.Fatal("frame leaks the authorization scheme to the relay")
	}

	// A relay that only has the wire bytes and its own fresh keys must not be
	// able to construct a session that opens them.
	eavesdropperStatic, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("eavesdropper keypair: %v", err)
	}
	eavesdropper, err := NewResponderSession(eavesdropperStatic)
	if err != nil {
		t.Fatalf("eavesdropper session: %v", err)
	}
	if _, err := eavesdropper.Open(sealed); err == nil {
		t.Fatal("a third party opened a frame it should not be able to read")
	}
}

// A relay is untrusted, so it is assumed to be actively malicious, not merely
// curious. Flipping a bit must be detected rather than silently delivered.
func TestTamperedFrameIsRejected(t *testing.T) {
	phone, desktop := handshake(t)

	sealed, err := phone.Seal([]byte("DELETE /v1/tasks/important"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01

	if _, err := desktop.Open(tampered); err == nil {
		t.Fatal("a tampered frame was accepted")
	}
}

// Noise nonces advance per message, so a captured frame cannot be replayed.
// Without this a relay could re-send a dispatch and start a run twice.
func TestReplayedFrameIsRejected(t *testing.T) {
	phone, desktop := handshake(t)

	first, err := phone.Seal([]byte("POST /v1/dispatch"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := desktop.Open(first); err != nil {
		t.Fatalf("first delivery should succeed: %v", err)
	}
	if _, err := desktop.Open(first); err == nil {
		t.Fatal("the same frame was accepted twice")
	}
}

// IK authenticates the responder: a phone that has the wrong desktop key must
// fail rather than establish a session with an impostor relay or host.
func TestHandshakeFailsAgainstTheWrongDesktopKey(t *testing.T) {
	realDesktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	impostor, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	// The phone believes it is talking to the real desktop.
	initiator, err := NewInitiatorSession(DesktopPublicKey(realDesktop))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	responder, err := NewResponderSession(impostor)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}

	first, err := initiator.StartHandshake()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := responder.ReadHandshake(first); err == nil {
		t.Fatal("an impostor completed a handshake meant for another desktop")
	}
}

// Using a session before the handshake finishes would send plaintext.
func TestSealBeforeHandshakeIsRefused(t *testing.T) {
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	phone, err := NewInitiatorSession(DesktopPublicKey(desktop))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}

	if phone.Established() {
		t.Fatal("a fresh session must not claim to be established")
	}
	if _, err := phone.Seal([]byte("secret")); err == nil {
		t.Fatal("sealing before the handshake completed must fail")
	}
}

// The keypair has to survive being written to disk on the desktop and to the
// Keystore on the phone, so it round trips through a portable encoding.
func TestKeypairRoundTripsThroughEncoding(t *testing.T) {
	original, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	encoded := EncodeKeypair(original)
	restored, err := DecodeKeypair(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if DesktopPublicKey(restored) != DesktopPublicKey(original) {
		t.Fatal("public key changed across a round trip")
	}

	// And a restored keypair still completes a handshake.
	responder, err := NewResponderSession(restored)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	initiator, err := NewInitiatorSession(DesktopPublicKey(original))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	first, err := initiator.StartHandshake()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := responder.ReadHandshake(first); err != nil {
		t.Fatalf("restored keypair failed a handshake: %v", err)
	}
}

// Android calls the core from several threads: a refresh on the main thread
// while the stream reader is delivering frames. flynn/noise mutates a nonce
// counter inside Encrypt, so unsynchronised use would reuse a ChaCha20-Poly1305
// nonce, which leaks the keystream and forges frames. Run with -race.
func TestConcurrentUseIsSafe(t *testing.T) {
	phone, desktop := handshake(t)

	var wg sync.WaitGroup
	sealed := make(chan []byte, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame, err := phone.Seal([]byte("concurrent request"))
			if err != nil {
				t.Errorf("seal: %v", err)
				return
			}
			sealed <- frame
		}()
	}
	wg.Wait()
	close(sealed)

	// Every frame must still be distinct: identical ciphertext would prove a
	// nonce was reused.
	seen := make(map[string]bool)
	for frame := range sealed {
		key := string(frame)
		if seen[key] {
			t.Fatal("two frames were identical, so a nonce was reused")
		}
		seen[key] = true
	}
	if len(seen) != 32 {
		t.Fatalf("expected 32 distinct frames, got %d", len(seen))
	}
	_ = desktop
}

// A relay that reorders frames is not the same threat as one that tampers, but
// Noise's per-message nonce means a reordered frame cannot be opened. The
// transport must therefore preserve order; this test pins that requirement so
// whoever writes the relay learns it here rather than in production.
func TestOutOfOrderFramesAreRejected(t *testing.T) {
	phone, desktop := handshake(t)

	first, err := phone.Seal([]byte("first"))
	if err != nil {
		t.Fatalf("seal first: %v", err)
	}
	second, err := phone.Seal([]byte("second"))
	if err != nil {
		t.Fatalf("seal second: %v", err)
	}

	if _, err := desktop.Open(second); err == nil {
		t.Fatal("a frame delivered out of order was accepted")
	}
	_ = first
}

// The initiator must not treat an unanswered handshake as established: doing so
// would accept a relay that never proved it holds the desktop key. The comment
// in FinishHandshake explains this; this test enforces it.
func TestInitiatorIsNotEstablishedWithoutAValidReply(t *testing.T) {
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	phone, err := NewInitiatorSession(DesktopPublicKey(desktop))
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	if _, err := phone.StartHandshake(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if phone.Established() {
		t.Fatal("established before the responder replied")
	}
	if err := phone.FinishHandshake([]byte("not a real reply")); err == nil {
		t.Fatal("accepted a bogus handshake reply")
	}
	if phone.Established() {
		t.Fatal("established after a failed handshake")
	}
	if _, err := phone.Seal([]byte("secret")); err == nil {
		t.Fatal("sealed after a failed handshake")
	}
}

// The phone must be able to read replies through the FFI, not just send. This
// exercises the full round trip over the handle API that Android actually uses.
func TestSessionHandleCanOpenReplies(t *testing.T) {
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	responder, err := NewResponderSession(desktop)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	handle, err := OpenInitiatorSession(DesktopPublicKey(desktop))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer CloseSession(handle)

	first, err := SessionStartHandshake(handle)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	raw, err := DecodeFrame(first)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	reply, err := responder.ReadHandshake(raw)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if err := SessionFinishHandshake(handle, EncodeFrame(reply)); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if !SessionEstablished(handle) {
		t.Fatal("handle should report an established session")
	}

	sealed, err := responder.Seal([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("desktop seal: %v", err)
	}
	opened, err := SessionOpen(handle, EncodeFrame(sealed))
	if err != nil {
		t.Fatalf("session open: %v", err)
	}
	if opened != `{"ok":true}` {
		t.Fatalf("phone read %q", opened)
	}

	if _, err := SessionOpen(handle, "!!!not base64!!!"); err == nil {
		t.Fatal("SessionOpen accepted junk instead of failing cleanly")
	}
	if _, err := SessionOpen(999999, EncodeFrame(sealed)); err == nil {
		t.Fatal("SessionOpen accepted an unknown handle")
	}
}

func TestDecodeKeypairRejectsJunk(t *testing.T) {
	for _, bad := range []string{"", "   ", "not-base64!!", "aGVsbG8="} {
		if _, err := DecodeKeypair(bad); err == nil {
			t.Fatalf("decoded junk %q", bad)
		}
	}
}

// gomobile binds only simple types, so sessions cross the FFI as handles and
// frames as base64 strings. This is the surface Android actually calls.
func TestSessionHandleAPIForMobile(t *testing.T) {
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	desktopPub := DesktopPublicKey(desktop)

	phoneHandle, err := OpenInitiatorSession(desktopPub)
	if err != nil {
		t.Fatalf("open initiator: %v", err)
	}
	defer CloseSession(phoneHandle)

	first, err := SessionStartHandshake(phoneHandle)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Base64 so it survives the string-only FFI boundary.
	if strings.ContainsAny(first, "\x00\n") {
		t.Fatal("handshake message must be transport safe text")
	}

	responder, err := NewResponderSession(desktop)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	raw, err := DecodeFrame(first)
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	reply, err := responder.ReadHandshake(raw)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if err := SessionFinishHandshake(phoneHandle, EncodeFrame(reply)); err != nil {
		t.Fatalf("finish: %v", err)
	}

	sealed, err := SessionSeal(phoneHandle, "hello")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	rawSealed, err := DecodeFrame(sealed)
	if err != nil {
		t.Fatalf("decode sealed: %v", err)
	}
	opened, err := responder.Open(rawSealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != "hello" {
		t.Fatalf("desktop read %q", opened)
	}
}

// A closed or unknown handle must fail cleanly rather than panic across FFI,
// where a panic would take the whole app down.
func TestUnknownSessionHandleFailsCleanly(t *testing.T) {
	if _, err := SessionSeal(999999, "x"); err == nil {
		t.Fatal("sealing on an unknown handle must fail")
	}
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	h, err := OpenInitiatorSession(DesktopPublicKey(desktop))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	CloseSession(h)
	if _, err := SessionStartHandshake(h); err == nil {
		t.Fatal("a closed handle must not be usable")
	}
}

func TestInitiatorRejectsABadDesktopKey(t *testing.T) {
	for _, bad := range []string{"", "nope", "YWJj"} {
		if _, err := NewInitiatorSession(bad); err == nil {
			t.Fatalf("accepted bad desktop key %q", bad)
		}
	}
}

// The desktop advertises its public key in the pairing QR. The phone must be
// able to read it out of the existing pairing payload.
func TestPairingPayloadCarriesTheDesktopKey(t *testing.T) {
	desktop, err := NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	pub := DesktopPublicKey(desktop)

	raw, err := ParsePairingURL("https://host.ts.net:8443/pair#token=abc123&relay=https://relay.example.com&key=" + pub)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.DesktopKey != pub {
		t.Fatalf("desktop key not carried through: %q", req.DesktopKey)
	}
	if req.RelayURL != "https://relay.example.com" {
		t.Fatalf("relay url not carried through: %q", req.RelayURL)
	}
}

// A QR code is scanned from whatever the camera is pointed at, so the relay URL
// in it is attacker-controlled. An unvalidated relay URL would let a malicious
// poster send the phone's traffic to a cleartext or internal endpoint. Noise
// keeps the contents unreadable either way, but a cleartext transport still
// leaks metadata and invites downgrade, so junk is rejected outright.
func TestHostileRelayURLsAreRejected(t *testing.T) {
	hostile := []string{
		"http://relay.example.com",      // cleartext
		"ws://relay.example.com",        // cleartext websocket
		"http://169.254.169.254/latest", // cloud metadata endpoint
		"file:///etc/passwd",            // not a network transport
		"javascript:alert(1)",           // not a URL we should ever follow
		"not a url at all",              // junk
		"https://",                      // no host
	}
	for _, raw := range hostile {
		got, err := ParsePairingURL("https://host.ts.net:8443/pair#token=abc&relay=" + url.QueryEscape(raw))
		if err != nil {
			continue // rejecting the whole code is an acceptable response
		}
		var req PairingRequest
		if err := json.Unmarshal([]byte(got), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.RelayURL != "" {
			t.Fatalf("hostile relay URL %q was accepted as %q", raw, req.RelayURL)
		}
	}
}

// One validator, three callers: the phone reading a QR, the desktop loading
// config, and the dialer. They must agree, so the rule is tested once here
// rather than three times in three shapes.
func TestValidateRelayURL(t *testing.T) {
	for _, bad := range []string{
		"", "   ",
		"http://relay.example.com",          // cleartext
		"ws://relay.example.com",            // cleartext websocket
		"relay.example.com",                 // no scheme
		"https://",                          // no host
		"https://192.0.2.1",                 // literal address
		"https://[::1]",                     // literal address, v6
		"https://relay",                     // not fully qualified
		"https://user:pw@relay.example.com", // credentials in the URL
		"://nonsense",
	} {
		if err := ValidateRelayURL(bad); err == nil {
			t.Errorf("accepted a bad relay URL %q", bad)
		}
	}

	for _, good := range []string{
		"https://redline-relay.croutoncreations.com",
		"https://relay.example.com:8443",
		"  https://relay.example.com  ", // surrounding space is the caller's, not an error
	} {
		if err := ValidateRelayURL(good); err != nil {
			t.Errorf("rejected a good relay URL %q: %v", good, err)
		}
	}
}

func TestHttpsRelayURLIsAccepted(t *testing.T) {
	got, err := ParsePairingURL("https://host.ts.net:8443/pair#token=abc&relay=" + url.QueryEscape("https://relay.example.com"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.RelayURL != "https://relay.example.com" {
		t.Fatalf("relay url: %q", req.RelayURL)
	}
}

// The desktop key travels in a URL fragment where '+' decodes to a space. A key
// containing '+' is common enough that it must survive; a mangled key that
// silently became a different key would be worse than a clear failure.
func TestDesktopKeyWithPlusSurvivesTheQR(t *testing.T) {
	// Find a generated key that actually contains the awkward characters.
	var pub string
	for i := 0; i < 200; i++ {
		kp, err := NewDesktopKeypair()
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		candidate := DesktopPublicKey(kp)
		if strings.ContainsAny(candidate, "+/") {
			pub = candidate
			break
		}
	}
	if pub == "" {
		t.Skip("no key with + or / generated; encoding may already be url-safe")
	}

	got, err := ParsePairingURL("https://host.ts.net:8443/pair#token=abc&key=" + url.QueryEscape(pub))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(got), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.DesktopKey != pub {
		t.Fatalf("key mangled in transit:\n got %q\nwant %q", req.DesktopKey, pub)
	}
	// And the recovered key must still be usable.
	if _, err := NewInitiatorSession(req.DesktopKey); err != nil {
		t.Fatalf("recovered key unusable: %v", err)
	}
}

// An older desktop emits no key or relay. Pairing must still work directly;
// only the relay is unavailable.
func TestPairingWithoutRelayFieldsStillWorks(t *testing.T) {
	raw, err := ParsePairingURL("https://host.ts.net:8443/pair#token=abc123")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var req PairingRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.PairingToken != "abc123" {
		t.Fatalf("token: %q", req.PairingToken)
	}
	if req.DesktopKey != "" || req.RelayURL != "" {
		t.Fatal("absent fields should stay empty rather than being invented")
	}
}
