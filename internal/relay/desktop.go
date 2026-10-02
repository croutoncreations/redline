package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	core "github.com/croutoncreations/redline/mobile/core"
	"github.com/flynn/noise"
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
		// This channel's cipher state is spent. The multiplexer drops only this
		// handler; a later frame can then begin a fresh authenticated handshake
		// without disturbing any other phone.
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
	encoded, err := encodeTunnelResponse(resp)
	if errors.Is(err, errResponseTooLarge) {
		// Reject before Seal advances the nonce. The small fallback is sealed
		// exactly once, so this channel and every other channel remain in sync.
		encoded, err = encodeTunnelResponse(TunnelResponse{Status: http.StatusBadGateway})
	}
	if err != nil {
		return nil, err
	}
	sealed, err := h.session.Seal(encoded)
	if err != nil {
		return nil, fmt.Errorf("seal response: %w", err)
	}
	// Defense in depth against a Noise suite overhead change. At this point the
	// nonce is spent, so the caller drops only this channel; the oversized bytes
	// must never be prefixed or offered to the shared host writer.
	if len(sealed) > maxTunnelPayload {
		return nil, fmt.Errorf("%w: sealed response is %d bytes", errResponseTooLarge, len(sealed))
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

// relayChannel identifies one phone on the relay's host WebSocket. An array is
// deliberately used as the map key so all eight bytes, including zeroes, are
// significant and no text encoding can merge distinct channels.
type relayChannel [relayChannelBytes]byte

const maxRelayChannels = 25

// channelTimer is the narrow timer seam used by the per-channel idle reaper.
// time.Timer satisfies it; tests can inject a deterministic clock and invoke
// eviction without sleeping. The afterFunc seam must follow time.AfterFunc's
// asynchronous callback contract: it must return before fn begins, because a
// newly-created timer is installed while the multiplexer lock is held and fn
// acquires that same lock.
type channelTimer interface {
	Stop() bool
	Reset(time.Duration) bool
}

type multiplexedSession struct {
	handler    *SessionHandler
	frames     chan []byte
	ctx        context.Context
	cancel     context.CancelFunc
	lastSeen   time.Time
	processing bool
	timer      channelTimer
}

// sessionMultiplexer owns all phone Noise states for one host WebSocket.
// Entries never survive a host reconnect. Each entry has one worker, which
// preserves frame order for that channel while allowing unrelated channels to
// wait on the local API independently.
type sessionMultiplexer struct {
	mu        sync.Mutex
	sessions  map[relayChannel]*multiplexedSession
	ctx       context.Context
	cancel    context.CancelFunc
	keypair   noise.DHKey
	forwarder *Forwarder
	writeMu   sync.Mutex
	write     func(context.Context, []byte) error
	now       func() time.Time
	idleAfter time.Duration
	afterFunc func(time.Duration, func()) channelTimer
	closed    bool
}

func newSessionMultiplexer(ctx context.Context, keypair noise.DHKey, forwarder *Forwarder, write func(context.Context, []byte) error) *sessionMultiplexer {
	muxCtx, cancel := context.WithCancel(ctx)
	return &sessionMultiplexer{
		sessions:  make(map[relayChannel]*multiplexedSession),
		ctx:       muxCtx,
		cancel:    cancel,
		keypair:   keypair,
		forwarder: forwarder,
		write:     write,
		now:       time.Now,
		idleAfter: idleTimeout,
		afterFunc: func(after time.Duration, fn func()) channelTimer { return time.AfterFunc(after, fn) },
	}
}

// Dispatch accepts one complete relay host frame. Frames shorter than the
// channel prefix or larger than the host-wire ceiling are connection-level
// protocol failures. An exactly eight-byte frame is an idempotent close for
// that channel. All other frames are copied before asynchronous processing.
func (m *sessionMultiplexer) Dispatch(frame []byte) error {
	if len(frame) < relayChannelBytes {
		return fmt.Errorf("host frame is %d bytes; channel requires %d", len(frame), relayChannelBytes)
	}
	if len(frame) > maxHostWireFrame {
		return fmt.Errorf("host frame is %d bytes; limit is %d", len(frame), maxHostWireFrame)
	}

	var channel relayChannel
	copy(channel[:], frame[:relayChannelBytes])
	if len(frame) == relayChannelBytes {
		m.drop(channel, nil)
		return nil
	}
	payload := append([]byte(nil), frame[relayChannelBytes:]...)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return context.Canceled
	}
	entry := m.sessions[channel]
	if entry == nil {
		// The relay contract admits at most 25 clients. Preserve existing,
		// authenticated sessions if a faulty relay violates that cap: the
		// excess channel is ignored rather than consuming unbounded state or
		// forcing every legitimate phone to reconnect.
		if len(m.sessions) >= maxRelayChannels {
			m.mu.Unlock()
			return nil
		}
		entryCtx, cancel := context.WithCancel(m.ctx)
		entry = &multiplexedSession{
			handler:  NewSessionHandler(m.keypair, m.forwarder),
			frames:   make(chan []byte, 2),
			ctx:      entryCtx,
			cancel:   cancel,
			lastSeen: m.now(),
		}
		m.sessions[channel] = entry
		entry.timer = m.afterFunc(m.idleAfter, func() { m.evictIfIdle(channel, entry) })
		go m.serve(channel, entry)
	} else {
		entry.lastSeen = m.now()
		entry.timer.Reset(m.idleAfter)
	}

	select {
	case entry.frames <- payload:
		m.mu.Unlock()
		return nil
	default:
		// A conforming phone has at most one request awaiting a response. A
		// full queue therefore means this channel is flooding or out of order.
		// Drop only its state; blocking the host reader here would let one phone
		// starve all others.
		delete(m.sessions, channel)
		entry.timer.Stop()
		m.mu.Unlock()
		entry.cancel()
		return nil
	}
}

func (m *sessionMultiplexer) serve(channel relayChannel, entry *multiplexedSession) {
	for {
		select {
		case <-entry.ctx.Done():
			return
		case frame := <-entry.frames:
			if !m.startProcessing(channel, entry) {
				return
			}
			reply, err := entry.handler.HandleFrame(entry.ctx, frame)
			if err != nil {
				// Authentication, handshake, or decryption failure spends only
				// this channel. No response is wire-safe because there may be no
				// authenticated cipher with which to seal it.
				m.drop(channel, entry)
				return
			}
			if err := m.writeResponse(channel, entry, reply); err != nil {
				// Production write failures close the host transport so readLoop
				// reconnects. A defensive size failure spends only this channel.
				m.drop(channel, entry)
				return
			}
			m.finishProcessing(channel, entry)
		}
	}
}

// writeResponse serializes the host WebSocket write, then checks channel
// identity while holding the session lock. A close or replacement that wins
// before the writer boundary makes this response stale and it is silently
// discarded. The host-scoped context is deliberate: canceling one channel
// must never turn its queued write into a shared transport failure.
func (m *sessionMultiplexer) writeResponse(channel relayChannel, entry *multiplexedSession, reply []byte) error {
	if len(reply) > maxTunnelPayload {
		return fmt.Errorf("%w: encrypted response is %d bytes", errResponseTooLarge, len(reply))
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sessions[channel] != entry {
		return nil
	}
	wire := make([]byte, relayChannelBytes+len(reply))
	copy(wire, channel[:])
	copy(wire[relayChannelBytes:], reply)
	return m.write(m.ctx, wire)
}

func (m *sessionMultiplexer) active(channel relayChannel, entry *multiplexedSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && m.sessions[channel] == entry
}

func (m *sessionMultiplexer) startProcessing(channel relayChannel, entry *multiplexedSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sessions[channel] != entry {
		return false
	}
	entry.processing = true
	return true
}

func (m *sessionMultiplexer) finishProcessing(channel relayChannel, entry *multiplexedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sessions[channel] != entry {
		return
	}
	entry.processing = false
	entry.lastSeen = m.now()
	entry.timer.Reset(m.idleAfter)
}

// drop removes expected only when it is still the current generation for the
// channel. That identity check prevents a late worker or idle callback from
// deleting a fresh session created after a close/reopen race. A nil expected
// means an authoritative channel-close frame and removes whichever generation
// is current.
func (m *sessionMultiplexer) drop(channel relayChannel, expected *multiplexedSession) {
	m.mu.Lock()
	entry := m.sessions[channel]
	if entry == nil || (expected != nil && entry != expected) {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, channel)
	entry.timer.Stop()
	m.mu.Unlock()
	entry.cancel()
}

func (m *sessionMultiplexer) evictIfIdle(channel relayChannel, expected *multiplexedSession) {
	m.mu.Lock()
	entry := m.sessions[channel]
	if entry == nil || entry != expected {
		m.mu.Unlock()
		return
	}
	if entry.processing {
		// A request waiting on the local API is active, not idle. This preserves
		// the old single-session rule that a slow forward cannot trip the idle
		// timeout while still letting other channels progress.
		entry.timer.Reset(m.idleAfter)
		m.mu.Unlock()
		return
	}
	remaining := m.idleAfter - m.now().Sub(entry.lastSeen)
	if remaining > 0 {
		// A Reset racing the old callback may let that callback run. Recheck
		// lastSeen and arm only the remaining duration rather than deleting a
		// channel that has just received traffic.
		entry.timer.Reset(remaining)
		m.mu.Unlock()
		return
	}
	delete(m.sessions, channel)
	m.mu.Unlock()
	entry.cancel()
}

func (m *sessionMultiplexer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// Close discards every channel and Noise state for this host connection.
func (m *sessionMultiplexer) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	entries := make([]*multiplexedSession, 0, len(m.sessions))
	for channel, entry := range m.sessions {
		delete(m.sessions, channel)
		entry.timer.Stop()
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	m.cancel()
	for _, entry := range entries {
		entry.cancel()
	}
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
