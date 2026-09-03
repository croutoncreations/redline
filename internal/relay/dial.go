package relay

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
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

// errIdle marks the ordinary end of a session, where no phone has sent
// anything for the idle timeout. It is separated from real failures because
// the two deserve opposite responses: reconnect promptly after a quiet spell,
// back off after an outage.
var errIdle = errors.New("idle timeout")

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
			// An idle timeout is the ordinary end of a session: the phone put
			// itself away. Treating it as a failure would grow the backoff, so
			// a desktop that had merely been quiet would then be slow to answer
			// the next time someone opened the app.
			if errors.Is(err, errIdle) {
				bo.reset()
				continue
			}
			// Anything else means the relay is unavailable or dropped us.
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

	// coder/websocket defaults to a 32 KB read limit, which a run's logs pass
	// routinely. Exceeding it does not fail the request: it closes the socket,
	// so the tunnel would drop mid-download and reconnect. Sized to the frame
	// ceiling plus room for Noise and JSON overhead.
	conn.SetReadLimit(maxTunnelFrame)

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
		readExpired := readCtx.Err() != nil && ctx.Err() == nil
		cancelRead()
		if err != nil {
			if ctx.Err() != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
				return nil
			}
			if !readExpired {
				// The socket failed rather than went quiet: the relay dropped
				// us, the network reset, or the object was evicted. This must
				// back off. Calling it idle and retrying immediately produced
				// twenty thousand dials in three seconds against a relay that
				// accepts and hangs up.
				_ = conn.CloseNow()
				return fmt.Errorf("read frame: %w", err)
			}
			// Only a genuine read deadline means the idle timeout
			// fired. Close normally and let Run reconnect (or wait for a phone
			// to come back).
			_ = conn.Close(websocket.StatusNormalClosure, "idle timeout")
			// IdleSince returns an instant, so report the elapsed time rather
			// than a wall clock timestamp, which read as if it were a duration.
			idleFor := time.Since(handler.IdleSince(connectedAt)).Round(time.Second)
			return fmt.Errorf("%w after %v", errIdle, idleFor)
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
// sessionURL builds the address this desktop dials.
//
// Built through net/url rather than concatenated. An entitlement token is
// standard base64, whose alphabet includes '+', and a query string decodes
// that as a space -- so concatenation would corrupt about half of all real
// tokens and tell a paying customer they had not paid. Encoding the path
// segment likewise stops a corrupted session id from adding its own query
// parameters or climbing out of the path.
//
// The returned URL carries a credential and must not be logged.
func (d *Dialer) sessionURL() string {
	base, err := url.Parse(d.opts.RelayURL)
	if err != nil {
		// Validated at config load, so this is unreachable in practice; a
		// deliberately broken URL simply fails to dial and backs off.
		return d.opts.RelayURL
	}
	base.Path = path.Join(base.Path, "/v1/session", d.opts.SessionID)
	query := url.Values{}
	query.Set("role", "host")
	if d.opts.EntitlementToken != "" {
		query.Set("entitlement", d.opts.EntitlementToken)
	}
	base.RawQuery = query.Encode()
	return base.String()
}
