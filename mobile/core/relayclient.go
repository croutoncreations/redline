package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// relayClientFrameLimit is the maximum size of a single WebSocket frame on the
// wire. Must match internal/relay's maxTunnelFrame (1 MB). Keeping a local
// constant is necessary because mobile/core cannot import internal/relay
// (internal package visibility). If the relay package changes this value, this
// constant must be updated to match, or the transport will silently clip large
// frames: the library closes the socket rather than returning an error, so the
// failure looks like an unstable relay rather than a size mismatch.
const relayClientFrameLimit = 1024 * 1024

// relayClientRequestTimeout is the default per-request timeout.
const relayClientRequestTimeout = 30 * time.Second

// relayClientHandshakeTimeout limits the initial Noise IK handshake.
//
// An impostor that cannot decrypt the initiator's message cannot reply, so the
// phone would hang forever on context.Background(). A genuine relay outage is
// indistinguishable from an impostor at dial time, so a short timeout is the
// right answer in both cases: fail fast and let the caller decide.
// 5 seconds is generous for a successful handshake over any reasonable
// connection, and fast enough for unit tests.
const relayClientHandshakeTimeout = 5 * time.Second

// tunnelRequest is the wire format for a request going phone → desktop.
// Must match internal/relay.TunnelRequest.
type tunnelRequest struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// tunnelResponse is the wire format for a reply going desktop → phone.
// Must match internal/relay.TunnelResponse.
type tunnelResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body,omitempty"`
}

// RelayClient is the phone's end of the Noise-encrypted relay tunnel.
//
// Each RelayClient owns exactly one Noise session and one WebSocket connection.
// Noise requires strictly ordered, exactly-once delivery: two concurrent writes
// would advance the nonce out of step and corrupt every subsequent frame. The
// mutex is held across the entire write + read pair, not just the field access.
//
// When the session is finished (by Close or a fatal error) the client is
// permanently unusable. Rule 1: sessions are single-use.
type RelayClient struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	session *NoiseSession
	closed  bool
	timeout time.Duration

	// authToken is the bearer credential for every request on this session.
	authToken string
}

// DialRelay connects to the relay, performs a full Noise IK handshake with the
// desktop, and returns a ready-to-use RelayClient.
//
// relayURL must be wss://, or ws:// only for loopback addresses (tests).
// sessionID must be 16–128 characters of [A-Za-z0-9_-].
// desktopPublicKey must be a valid base64-encoded 32-byte Curve25519 key.
// entitlementToken may be empty during development.
//
// The function fails if the handshake does not complete, which means the far
// end did not hold the expected desktop private key: an impostor relay or a
// misconfigured desktop is caught here rather than silently passing requests to
// the wrong party.
func DialRelay(relayURL, sessionID, desktopPublicKey, entitlementToken string) (*RelayClient, error) {
	if err := validateDialInputs(relayURL, sessionID, desktopPublicKey); err != nil {
		return nil, err
	}

	session, err := NewInitiatorSession(desktopPublicKey)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}

	target := buildSessionURL(relayURL, sessionID, entitlementToken)

	conn, _, err := websocket.Dial(context.Background(), target, nil)
	if err != nil {
		return nil, fmt.Errorf("dial relay: connect failed")
	}

	// Rule 4: set the read limit to the frame ceiling. The library default is
	// 32 KB; exceeding it closes the socket rather than failing one read, which
	// would look like a broken relay.
	conn.SetReadLimit(relayClientFrameLimit)

	handshakeMsg, err := session.StartHandshake()
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("dial relay: start handshake: %w", err)
	}

	if err := conn.Write(context.Background(), websocket.MessageBinary, handshakeMsg); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("dial relay: write handshake: %w", err)
	}

	// A context with deadline so an impostor that cannot decrypt our message
	// (and therefore cannot reply) does not leave us blocked forever. A genuine
	// unavailable relay is indistinguishable from an impostor here, and the
	// right answer in both cases is to fail fast and let the caller retry.
	readCtx, cancelRead := context.WithTimeout(context.Background(), relayClientHandshakeTimeout)
	defer cancelRead()

	_, reply, err := conn.Read(readCtx)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("dial relay: read handshake reply: %w", err)
	}
	// FinishHandshake authenticates the far end. If the far end holds a
	// different key than desktopPublicKey, this call fails with a decrypt
	// error, and the connection is refused. This is the impostor check.
	if err := session.FinishHandshake(reply); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("dial relay: handshake authentication failed: %w", err)
	}

	return &RelayClient{
		conn:    conn,
		session: session,
		timeout: relayClientRequestTimeout,
	}, nil
}

// SetTimeoutSeconds sets the per-request timeout. The default is 30 seconds.
// A value ≤ 0 is ignored.
func (c *RelayClient) SetTimeoutSeconds(seconds int) {
	if seconds <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timeout = time.Duration(seconds) * time.Second
}

// SetAuthToken sets the bearer token sent with every request.
//
// Every Redline endpoint requires one, so a tunnel without this reaches the
// desktop and is turned away by it -- which looks like a broken relay and is
// really a missing header. Held on the client rather than passed per request
// because the token belongs to the session, not to any one call.
func (c *RelayClient) SetAuthToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authToken = strings.TrimSpace(token)
}

// Request sends a single HTTP-like request through the tunnel and returns the
// response body as a string.
//
// The mutex is held across the entire write + read pair because Noise nonces
// advance per message: a second concurrent write would send the same nonce
// twice and break all decryption permanently.
//
// A non-2xx status is returned as an error alongside the body. A malformed
// response (bad JSON after a successful decrypt) is returned as an error but
// does NOT close the session: the Noise cipher state is still in sync because
// the frame decrypted correctly — only the application payload was bad.
//
// Rule 2: frames must arrive exactly once and in order. The mutex on this side,
// plus the relay's ordered delivery, ensures that.
// Rule 3: a failed request is never resent on this session. The caller must
// decide whether to retry (with a fresh DialRelay if the session is closed).
func (c *RelayClient) Request(method, reqPath, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return "", errors.New("relay client is closed")
	}

	req := tunnelRequest{
		Method: method,
		Path:   reqPath,
	}
	if c.authToken != "" {
		req.Header = map[string][]string{"Authorization": {"Bearer " + c.authToken}}
	}
	if body != "" {
		req.Body = []byte(body)
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	frame, err := c.session.Seal(encoded)
	if err != nil {
		// A Seal failure means the session is broken — close it.
		c.closeConn()
		return "", fmt.Errorf("seal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		c.closeConn()
		return "", fmt.Errorf("write request: %w", err)
	}

	_, rawFrame, err := c.conn.Read(ctx)
	if err != nil {
		c.closeConn()
		return "", fmt.Errorf("read response: %w", err)
	}

	plaintext, err := c.session.Open(rawFrame)
	if err != nil {
		// A decrypt failure means the Noise session is permanently out of step
		// (rule 1). Close so the caller knows to re-dial.
		c.closeConn()
		return "", fmt.Errorf("decrypt response: %w", err)
	}

	// The frame decrypted correctly, so the cipher state is still in sync.
	// A JSON decode failure is an application-level error, not a session error.
	// Do NOT close the session here — the next Request will work.
	var resp tunnelResponse
	if err := json.Unmarshal(plaintext, &resp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	bodyStr := string(resp.Body)
	if resp.Status < 200 || resp.Status >= 300 {
		return bodyStr, fmt.Errorf("relay response status %d", resp.Status)
	}
	return bodyStr, nil
}

// Close shuts down the relay session and connection. Safe to call more than
// once. After Close, Request returns an error immediately.
//
// Rule 1: sessions are single-use. Close marks this client as permanently
// finished; DialRelay must be called again to establish a new session.
func (c *RelayClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeConn()
}

// closeConn performs the actual teardown. Must be called with c.mu held.
func (c *RelayClient) closeConn() {
	if c.closed {
		return
	}
	c.closed = true
	if c.conn != nil {
		c.conn.CloseNow()
	}
}

// validateDialInputs checks the relay URL, session id, and desktop public key
// before anything is dialled.
func validateDialInputs(relayURL, sessionID, desktopPublicKey string) error {
	if err := validateRelayURLForDial(relayURL); err != nil {
		return fmt.Errorf("invalid relay URL: %w", err)
	}
	if safeSessionID(sessionID) == "" {
		return fmt.Errorf("invalid session ID: must be 16–128 characters of [A-Za-z0-9_-]")
	}
	if desktopPublicKey == "" {
		return errors.New("desktop public key is required")
	}
	// Let NewInitiatorSession do the key validation: it checks base64 and
	// length precisely. We don't need a separate check here.
	if _, err := NewInitiatorSession(desktopPublicKey); err != nil {
		return fmt.Errorf("invalid desktop public key: %w", err)
	}
	return nil
}

// validateRelayURLForDial accepts wss:// always, and ws:// only for loopback
// addresses (used in tests against httptest.Server).
//
// ValidateRelayURL requires https:// because the relay is on the public
// internet. Here we also accept ws:// for loopback to allow unit tests using
// httptest.NewServer without TLS.
func validateRelayURLForDial(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("relay URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL", raw)
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := parsed.Hostname()

	switch scheme {
	case "wss":
		// Always allowed.
	case "ws":
		// Only for loopback — tests use httptest.Server which gives http/ws.
		if !isLoopbackHost(host) && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("%q must use wss:// for non-loopback hosts", raw)
		}
	default:
		return fmt.Errorf("%q must use wss:// (or ws:// for loopback only)", raw)
	}

	if host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("%q must not contain credentials", raw)
	}
	return nil
}

// buildSessionURL constructs the WebSocket URL for the phone's session.
//
// Built through net/url rather than concatenation. Entitlement tokens are
// standard base64, whose alphabet includes '+'; concatenation would decode
// that as a space on the Worker and corrupt the token.
func buildSessionURL(relayURL, sessionID, entitlementToken string) string {
	base, err := url.Parse(relayURL)
	if err != nil {
		return relayURL
	}
	base.Path = path.Join(base.Path, "/v1/session", sessionID)
	query := url.Values{}
	query.Set("role", "client")
	if entitlementToken != "" {
		query.Set("entitlement", entitlementToken)
	}
	base.RawQuery = query.Encode()
	return base.String()
}
