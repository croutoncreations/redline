package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
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

	// EntitlementTokenSource returns the current runtime bearer credential for
	// every handshake, including reconnects performed inside one Dialer.Run.
	// Hosted callers must supply a live source rather than capturing a token.
	EntitlementTokenSource func() string

	// The relay verifies Ed25519 authenticity, expiry, and session-bound claims.

	// HTTPClient bounds WebSocket handshakes. Redirects are always rejected by
	// the dialer before an entitlement header can be forwarded.
	HTTPClient *http.Client

	// EntitlementSignal reports only typed relay entitlement events. It never
	// receives an error string, header, or token.
	EntitlementSignal func(EntitlementSignal)

	// Logf reports connection state to the operator. Nil means silent, which
	// is what every test that does not care about output gets.
	//
	// The dial loop used to have no output at all, and a relay that was
	// working looked exactly like one refusing every connection: the only way
	// to tell them apart was to open a second session and see whether the
	// relay answered 409. Every line written here passes through redactToken
	// first as defense in depth against a library echoing handshake headers.
	Logf func(format string, args ...any)
}

// Dialer is the desktop's outbound relay connection manager.
//
// It dials the relay, authenticates phones over Noise IK, and forwards their
// requests to the local API. When the connection drops it backs off and
// reconnects; when the context is cancelled it stops.
type Dialer struct {
	opts DialerOptions
}

type EntitlementSignal string

const (
	EntitlementHandshakeRequired EntitlementSignal = "handshake_402"
	EntitlementExpired           EntitlementSignal = "close_1008_expired"
)

type EntitlementSignalError struct{ Signal EntitlementSignal }

func (e *EntitlementSignalError) Error() string {
	return "relay requires entitlement renewal: " + string(e.Signal)
}

// errBadFrame marks a frame this desktop could not read, which ends the Noise
// session but says nothing about the relay's health.
//
// Separated from a transport failure because the two deserve opposite
// responses: reconnect at once after a bad frame, back off after an outage.
var errBadFrame = errors.New("handle frame")

// NewDialer creates a Dialer from the given options.
func NewDialer(opts DialerOptions) *Dialer {
	return &Dialer{opts: opts}
}

// logf reports to the operator, with the entitlement token stripped.
//
// Redaction happens here rather than at each call site so that adding a log
// line later cannot leak the credential: the only way to write output from
// this type is through a function that has already removed it.
func (d *Dialer) signal(signal EntitlementSignal) {
	if d.opts.EntitlementSignal != nil {
		d.opts.EntitlementSignal(signal)
	}
}

func (d *Dialer) currentToken() string {
	if d.opts.EntitlementTokenSource == nil {
		return ""
	}
	return d.opts.EntitlementTokenSource()
}

func (d *Dialer) logf(format string, args ...any) {
	if d.opts.Logf == nil {
		return
	}
	d.opts.Logf("%s", redactToken(fmt.Sprintf(format, args...), d.currentToken()))
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
			if errors.Is(err, errBadFrame) {
				// A malformed host envelope is a protocol reset rather than a
				// relay outage, so reconnect promptly with an empty channel map.
				bo.reset()
				d.logf("relay: %v; reconnecting", err)
				continue
			}
			// Anything else means the relay is unavailable or dropped us.
			// Sleep the backoff, then try again.
			wait := bo.next()
			// Reported at every attempt rather than only the first: a relay
			// that is down stays down quietly, and the growing interval is
			// the only signal that retries are still happening at all.
			d.logf("relay: %v; retrying in %s", err, wait.Round(time.Millisecond))
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
	if err := ValidateSessionID(d.opts.SessionID); err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	target := d.sessionURL()
	client := d.handshakeClient()
	// Snapshot once for this handshake. The next internal reconnect loads the
	// source again, while errors from this attempt are redacted with the exact
	// credential that was sent even if renewal races the dial.
	token := d.currentToken()
	conn, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client, HTTPHeader: entitlementHeader(token)})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		if response != nil && response.StatusCode == http.StatusPaymentRequired {
			d.signal(EntitlementHandshakeRequired)
			return &EntitlementSignalError{Signal: EntitlementHandshakeRequired}
		}
		// Library errors may echo handshake request details. Nothing logs this
		// today, and that is exactly why the token is stripped here: the leak
		// stays invisible until someone adds a log line, and then it is a
		// credential in a file. Redacting at the source is cheap defense in
		// depth even though the token travels only in a header.
		return fmt.Errorf("dial relay: %s", redactToken(err.Error(), token, d.currentToken()))
	}
	defer conn.CloseNow()

	// The session id, not the URL: naming the session is what an operator
	// actually needs to correlate this desktop with a phone or relay-side 409,
	// and avoids logging incidental URL details.
	d.logf("relay: connected, session %s", d.opts.SessionID)

	// coder/websocket defaults to a 32 KB read limit, which a run's logs pass
	// routinely. The relay prefixes each host-bound payload with its eight-byte
	// channel, so the host wire limit is deliberately larger than the unchanged
	// 1 MiB tunnel payload limit. readLoop consumes that prefix before
	// dispatching the payload to its per-channel Noise handler.
	conn.SetReadLimit(maxHostWireFrame)

	// readLoop creates a fresh channel map for this connection. No Noise state
	// survives a reconnect, even when the relay assigns a phone the same
	// channel bytes again.
	if err := d.readLoop(ctx, conn); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

// readLoop demultiplexes frames from one host socket. Each channel owns a
// serial worker and independent Noise responder; writes share one mutex because
// coder/websocket permits only one active writer. Channel-level protocol or
// authentication failures delete that channel, while malformed host envelopes
// reconnect the whole host because their channel cannot be identified safely.
func (d *Dialer) readLoop(ctx context.Context, conn *websocket.Conn) error {
	var writeMu sync.Mutex
	mux := newSessionMultiplexer(ctx, d.opts.Keypair, d.opts.Forwarder, func(writeCtx context.Context, frame []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.Write(writeCtx, websocket.MessageBinary, frame); err != nil {
			// Unblock the sole reader so connect can discard every channel and
			// enter the normal reconnect path.
			_ = conn.CloseNow()
			return err
		}
		return nil
	})
	defer mux.Close()

	for {
		if ctx.Err() != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
			return nil
		}

		_, frame, err := conn.Read(ctx)
		if err != nil {
			// Close 1008 is the relay's documented authoritative entitlement
			// renewal contract. Do not couple the typed signal to human text.
			if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
				d.signal(EntitlementExpired)
				return &EntitlementSignalError{Signal: EntitlementExpired}
			}
			if ctx.Err() != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "context cancelled")
				return nil
			}
			if websocket.CloseStatus(err) == websocket.StatusMessageTooBig {
				return fmt.Errorf("%w: host frame exceeds %d bytes", errBadFrame, maxHostWireFrame)
			}
			// The socket failed rather than one phone's channel. Back off so a
			// relay that accepts and immediately drops hosts cannot cause a hot
			// reconnect loop.
			_ = conn.CloseNow()
			return fmt.Errorf("read frame: %w", err)
		}

		if err := mux.Dispatch(frame); err != nil {
			status := websocket.StatusProtocolError
			reason := "host frame missing channel"
			if len(frame) > maxHostWireFrame {
				status = websocket.StatusMessageTooBig
				reason = "host frame too large"
			}
			_ = conn.Close(status, reason)
			return fmt.Errorf("%w: %w", errBadFrame, err)
		}
	}
}

// sessionURL builds the relay WebSocket URL for this session.
// redactToken removes an entitlement token from a message.
//
// Takes the token rather than pattern-matching a URL, so it cannot be fooled by
// a different encoding of the same value.
func redactToken(message string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
		// A library error may URL-escape a credential even though Redline never
		// deliberately puts either the license or token in a URL.
		message = strings.ReplaceAll(message, url.QueryEscape(secret), "[redacted]")
	}
	return message
}

// sessionURL builds the address this desktop dials.
//
// Encoding the path segment stops a corrupted session id from adding its own
// query parameters or climbing out of the path. The entitlement is deliberately
// absent: it travels in a handshake header so infrastructure URL logs cannot
// retain the bearer credential.
func (d *Dialer) sessionHeaders() http.Header { return entitlementHeader(d.currentToken()) }

func entitlementHeader(token string) http.Header {
	headers := http.Header{}
	if token != "" {
		headers.Set("X-Redline-Entitlement", token)
	}
	return headers
}

func (d *Dialer) handshakeClient() *http.Client {
	client := d.opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	if client.Timeout <= 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	// coder/websocket preserves the supplied CheckRedirect. Refusing every
	// redirect guarantees the bearer header never crosses an origin boundary.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ValidateSessionID applies the relay's public path-segment contract.
func ValidateSessionID(sessionID string) error {
	if !sessionIDPattern.MatchString(sessionID) {
		return errors.New("relay session id must contain 16 to 128 base64url characters")
	}
	return nil
}

func (d *Dialer) sessionURL() string {
	base, err := url.Parse(d.opts.RelayURL)
	if err != nil {
		// Validated at config load, so this is unreachable in practice; a
		// deliberately broken URL simply fails to dial and backs off.
		return d.opts.RelayURL
	}
	prefix := path.Join(base.Path, "v1", "session")
	sessionID := d.opts.SessionID
	if ValidateSessionID(sessionID) != nil {
		// sessionURL remains safe for diagnostics even though connect rejects the
		// invalid public input before any request is sent.
		sessionID = "invalid-session-id"
	}
	base.Path = prefix + "/" + sessionID
	base.RawPath = (&url.URL{Path: prefix}).EscapedPath() + "/" + url.PathEscape(sessionID)
	query := url.Values{}
	query.Set("role", "host")
	base.RawQuery = query.Encode()
	return base.String()
}
