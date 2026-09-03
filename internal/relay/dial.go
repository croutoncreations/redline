package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
)

// DialerOptions configures the desktop's outbound relay leg.
type DialerOptions struct {
	// RelayURL is the WebSocket base URL of the relay (ws:// or wss://).
	RelayURL string

	// SessionID is the shared identifier that paired phones use to find this
	// desktop on the relay. It is persisted in config so a restart rejoins the
	// same session rather than stranding a phone on an id nobody answers.
	SessionID string

	// Keypair is the desktop's static Noise identity. Every frame it sends is
	// authenticated with the private half; every paired phone trusts the public
	// half.
	Keypair noise.DHKey

	// Forwarder replays phone requests against the desktop's local API.
	Forwarder *Forwarder

	// EntitlementToken authorises this desktop to use the relay. The relay
	// checks only that it is signed and unexpired, not who the holder is.
	// Left empty during development with ALLOW_UNENTITLED=true on the relay.
	EntitlementToken string
}

// Dialer is the desktop's outbound relay connection manager.
//
// It dials the relay, authenticates phones over Noise IK, and forwards their
// requests to the local API. When the connection drops it backs off and
// reconnects; when the context is cancelled it stops.
type Dialer struct {
	opts DialerOptions
}

// NewDialer creates a Dialer from the given options.
func NewDialer(opts DialerOptions) *Dialer {
	return &Dialer{opts: opts}
}

// Run dials the relay and serves frames until ctx is cancelled.
//
// It blocks, retrying with exponential backoff on every dial or frame error.
// Cancel ctx to stop it; it will return within one read timeout after the
// cancellation arrives.
func (d *Dialer) Run(ctx context.Context) {
	bo := newBackoff()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := d.connect(ctx); err != nil {
			// connect returns nil only when ctx is done.
			if ctx.Err() != nil {
				return
			}
			// Any other error means the relay is unavailable or dropped us.
			// Sleep the backoff, then try again.
			wait := bo.next()
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		// A nil return from connect means ctx was cancelled inside the read loop.
		bo.reset()
		return
	}
}

// connect dials the relay once, runs the frame loop until a fatal error, and
// returns that error. It returns nil if and only if ctx was cancelled.
func (d *Dialer) connect(ctx context.Context) error {
	target := d.sessionURL()
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.CloseNow()

	// A fresh SessionHandler for every connection: a resumed connection must
	// never reuse cipher states. A reconnect after a network blip creates a
	// new Noise session with the same static keypair, not a continuation of
	// the broken one. Using a stale HandshakeState or cipher object would
	// either fail to decrypt or, worse, silently produce wrong output.
	handler := NewSessionHandler(d.opts.Keypair, d.opts.Forwarder)

	if err := d.readLoop(ctx, conn, handler); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

// readLoop reads frames from conn, hands each to handler, and writes the
// reply. It returns when ctx is cancelled, when the idle timeout fires, or
// when a fatal frame error occurs.
func (d *Dialer) readLoop(ctx context.Context, conn *websocket.Conn, handler *SessionHandler) error {
	connectedAt := time.Now()
	for {
		if ctx.Err() != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
			return nil
		}

		// Enforce the idle timeout with a per-read context deadline. A socket
		// that goes quiet for idleTimeout must not block the loop forever: the
		// phone may be gone and the relay may be silently holding the
		// connection open. Wrapping only the read (not HandleFrame or Write)
		// means a slow local API call does not trigger an idle disconnect.
		readCtx, cancelRead := context.WithTimeout(ctx, idleTimeout)
		_, frame, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			if ctx.Err() != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
				return nil
			}
			// A deadline exceeded on the read ctx means the idle timeout
			// fired. Close normally and let Run reconnect (or wait for a phone
			// to come back).
			_ = conn.Close(websocket.StatusNormalClosure, "idle timeout")
			return fmt.Errorf("idle timeout after %v", handler.IdleSince(connectedAt))
		}

		reply, err := handler.HandleFrame(ctx, frame)
		if err != nil {
			// A decrypt failure terminates the Noise session: the cipher
			// states are out of sync and every subsequent frame would be
			// wrong. Close and reconnect so the phone can start a fresh
			// handshake.
			_ = conn.Close(websocket.StatusProtocolError, "frame error")
			return fmt.Errorf("handle frame: %w", err)
		}

		if err := conn.Write(ctx, websocket.MessageBinary, reply); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("write reply: %w", err)
		}
	}
}

// sessionURL builds the relay WebSocket URL for this session.
func (d *Dialer) sessionURL() string {
	base := d.opts.RelayURL
	u := base + "/v1/session/" + d.opts.SessionID + "?role=host"
	if d.opts.EntitlementToken != "" {
		// The token is not logged: sessionURL is called without printing u,
		// and Run does not log the returned URL.
		u += "&entitlement=" + d.opts.EntitlementToken
	}
	return u
}
