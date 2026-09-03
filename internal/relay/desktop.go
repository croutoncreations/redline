package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flynn/noise"
	core "github.com/jfox/redline/mobile/core"
)

// SessionHandler is the desktop-side session state machine.
//
// A relay is untrusted infrastructure. Every frame it forwards is treated as
// hostile input: the handshake authenticates the phone before any local API
// call is made, garbage frames return errors instead of panics, and a second
// handshake on an established session is refused rather than accepted as a
// reset. Silently accepting a state-reset frame from the relay would let the
// relay renegotiate to a key it controls.
type SessionHandler struct {
	mu        sync.Mutex
	session   *core.NoiseSession
	forwarder *Forwarder

	// lastSeen is the time of the most recent frame (handshake or application).
	// It is zero until the first frame arrives; callers use IdleSince to
	// convert "zero" into a meaningful fallback.
	lastSeen time.Time
}

// NewSessionHandler creates a handler that holds a fresh Noise responder
// session waiting for a phone to connect.
func NewSessionHandler(keypair noise.DHKey, forwarder *Forwarder) *SessionHandler {
	// The session returned here is always a responder: the desktop waits for
	// whoever arrives. An error here means the library failed to initialise a
	// Curve25519 state from a keypair we generated ourselves, which should not
	// happen; if it does, we panic rather than silently return a handler that
	// will fail on every frame.
	session, err := core.NewResponderSession(keypair)
	if err != nil {
		panic(fmt.Sprintf("relay: NewSessionHandler: NewResponderSession: %v", err))
	}
	return &SessionHandler{
		session:   session,
		forwarder: forwarder,
	}
}

// HandleFrame drives the session state machine with a single frame, using the
// real wall clock to update the idle timer.
func (h *SessionHandler) HandleFrame(ctx context.Context, frame []byte) ([]byte, error) {
	return h.HandleFrameAt(ctx, frame, time.Now())
}

// HandleFrameAt is HandleFrame with an injected clock, for tests that need to
// fast-forward time without sleeping.
func (h *SessionHandler) HandleFrameAt(ctx context.Context, frame []byte, at time.Time) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// A frame arriving before the handshake completes is either the legitimate
	// initiator handshake message or an attacker probing the session. The state
	// machine processes it as a handshake attempt; anything that is not a valid
	// Noise IK message will fail inside ReadHandshake and bubble up as an error.
	if !h.session.Established() {
		reply, err := h.session.ReadHandshake(frame)
		if err != nil {
			return nil, fmt.Errorf("handshake: %w", err)
		}
		h.updateLastSeen(at)
		return reply, nil
	}

	// Once the session is established, a further handshake frame would mean the
	// relay is trying to renegotiate or replay the initiator's first message to
	// trigger a state reset. Either way, we refuse rather than serve.
	//
	// How do we tell a second handshake from an application frame? We do not
	// have to: Open will fail to decrypt a raw Noise handshake message, so the
	// decrypt error is the refusal. A caller that sends a genuine second
	// handshake gets the same error as one that sends garbage, which is the
	// right answer in both cases.
	plaintext, err := h.session.Open(frame)
	if err != nil {
		// A decrypt failure terminates the session: the nonce advanced and the
		// cipher states are now out of sync. Continuing would produce wrong
		// results on every subsequent frame, which is worse than closing.
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	// A frame that decrypted is authenticated traffic from the paired phone,
	// whatever happens to it next. The idle clock is about whether anyone is
	// still there, so it advances here rather than only on success -- otherwise
	// a phone actively retrying while the local API is down would be reaped as
	// idle, which is exactly the outage the sealed 502 path exists to survive.
	h.updateLastSeen(at)

	req, err := DecodeRequest(plaintext)
	if err != nil {
		// A frame that decrypted successfully but does not contain a valid
		// request is a client bug rather than a relay attack. Return a sealed
		// error response so the phone sees a legible failure; do not close the
		// session.
		return h.sealResponse(TunnelResponse{Status: http.StatusBadRequest})
	}

	resp, err := h.forwarder.Forward(ctx, req)
	if err != nil {
		// A local API failure (handler down, network error) is not the phone's
		// fault and does not compromise the session. Return a sealed 502.
		return h.sealResponse(TunnelResponse{Status: http.StatusBadGateway})
	}

	return h.sealResponse(resp)
}

// sealResponse encodes a TunnelResponse and seals it for the phone. It must be
// called while h.mu is held.
func (h *SessionHandler) sealResponse(resp TunnelResponse) ([]byte, error) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("encode response: %w", err)
	}
	sealed, err := h.session.Seal(encoded)
	if err != nil {
		return nil, fmt.Errorf("seal response: %w", err)
	}
	return sealed, nil
}

// updateLastSeen records the most recent traffic time. Called while h.mu is
// held.
func (h *SessionHandler) updateLastSeen(at time.Time) {
	if at.After(h.lastSeen) {
		h.lastSeen = at
	}
}

// IdleSince returns when the handler last saw traffic. Before any frame has
// been processed, lastSeen is zero; in that case the caller's fallback is
// returned so "idle since the handler was created" maps to a meaningful time
// rather than the zero time.
func (h *SessionHandler) IdleSince(fallback time.Time) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lastSeen.IsZero() {
		return fallback
	}
	return h.lastSeen
}

// EncodeRequestParts encodes an HTTP request's parts into a tunnel frame.
//
// This is a convenience wrapper over the TunnelRequest type that avoids
// constructing an *http.Request just to encode it. The format is identical to
// EncodeRequest, so the phone and desktop are always speaking the same frame
// dialect.
func EncodeRequestParts(method, path string, header http.Header, body []byte) ([]byte, error) {
	return json.Marshal(TunnelRequest{
		Method: method,
		Path:   path,
		Header: header,
		Body:   body,
	})
}

// LoadOrCreateKeypair loads the desktop's Noise static keypair from path, or
// generates and persists a new one if no file exists.
//
// The keypair is the desktop's identity: every paired phone trusts its public
// key. Silently regenerating it on a corrupt file would unpair every phone
// without any warning, which is why a corrupt file is an error. The caller
// decides whether to repair or abort.
//
// The file is written with mode 0600 so only the owner can read the private
// key. Parent directories are created with mode 0700 for the same reason.
func LoadOrCreateKeypair(path string) (noise.DHKey, error) {
	// Check whether the file exists. os.IsNotExist is the right predicate;
	// any other error (permissions, IO) is surfaced so the caller can act.
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return noise.DHKey{}, fmt.Errorf("read keypair file: %w", err)
		}
		// File does not exist: generate a fresh keypair and persist it.
		return generateAndSaveKeypair(path)
	}

	// File exists: parse it. A parse failure is always an error; we do not
	// silently replace it because the replacement would have a different public
	// key, unpairing every phone that scanned the QR with the old one.
	kp, err := core.DecodeKeypair(string(data))
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("keypair file is corrupt: %w", err)
	}
	return kp, nil
}

// generateAndSaveKeypair creates a new keypair and writes it to path with
// mode 0600. Parent directories are created with mode 0700.
func generateAndSaveKeypair(path string) (noise.DHKey, error) {
	kp, err := core.NewDesktopKeypair()
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("generate keypair: %w", err)
	}

	encoded := core.EncodeKeypair(kp)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return noise.DHKey{}, fmt.Errorf("create keypair directory: %w", err)
	}

	// WriteFile with 0600 prevents any other user from reading the private key.
	// On Unix, the umask may further restrict permissions but can never open
	// them; 0600 is the ceiling we want.
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return noise.DHKey{}, fmt.Errorf("write keypair file: %w", err)
	}

	return kp, nil
}

// fileMode returns the permission bits of a file. It is used by tests to
// verify that the keypair file is not world-readable.
func fileMode(path string) (fs.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}

// writeFileForTest writes content to path, creating the file if necessary. It
// is used by tests to plant a corrupt keypair file that LoadOrCreateKeypair
// must reject.
func writeFileForTest(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
