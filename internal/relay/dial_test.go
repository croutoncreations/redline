package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
	core "github.com/jfox/redline/mobile/core"
)

// These tests run the desktop's dial loop against a stand-in relay, so the
// piece that was previously only scaffolding is exercised end to end.

// fakeRelay accepts one websocket and hands it to the test.
func fakeRelay(t *testing.T, onConn func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		onConn(conn)
	}))
}

func TestDialerServesAPhoneThroughTheRelay(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	// The relay stands in for Cloudflare: it takes the desktop's socket and
	// drives a phone conversation across it.
	done := make(chan error, 1)
	var once sync.Once
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		once.Do(func() {
			go func() {
				ctx := context.Background()
				phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
				if err != nil {
					done <- err
					return
				}
				first, err := phone.StartHandshake()
				if err != nil {
					done <- err
					return
				}
				channel := [relayChannelBytes]byte{1, 2, 3, 4, 5, 6, 7, 8}
				if err := conn.Write(ctx, websocket.MessageBinary, append(channel[:], first...)); err != nil {
					done <- err
					return
				}
				_, reply, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if len(reply) < relayChannelBytes || string(reply[:relayChannelBytes]) != string(channel[:]) {
					done <- errors.New("handshake reply used the wrong relay channel")
					return
				}
				if err := phone.FinishHandshake(reply[relayChannelBytes:]); err != nil {
					done <- err
					return
				}

				request, err := EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
				if err != nil {
					done <- err
					return
				}
				sealed, err := phone.Seal(request)
				if err != nil {
					done <- err
					return
				}
				if err := conn.Write(ctx, websocket.MessageBinary, append(channel[:], sealed...)); err != nil {
					done <- err
					return
				}
				_, frame, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if len(frame) < relayChannelBytes || string(frame[:relayChannelBytes]) != string(channel[:]) {
					done <- errors.New("response used the wrong relay channel")
					return
				}
				opened, err := phone.Open(frame[relayChannelBytes:])
				if err != nil {
					done <- err
					return
				}
				resp, err := DecodeResponse(opened)
				if err != nil {
					done <- err
					return
				}
				if resp.Status != http.StatusOK || string(resp.Body) != `{"ok":true}` {
					done <- errContext("unexpected response", resp.Status, string(resp.Body))
					return
				}
				done <- nil
			}()
		})
	})
	defer relay.Close()

	dialer := NewDialer(DialerOptions{
		RelayURL:  strings.Replace(relay.URL, "http://", "ws://", 1),
		SessionID: "test-session-id-0123456789",
		Keypair:   keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go dialer.Run(ctx)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("phone conversation failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out before the phone got a response")
	}
}

func TestDialerSurfacesTypedEntitlementHandshakeAndExpirySignals(t *testing.T) {
	t.Run("handshake 402", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusPaymentRequired)
		}))
		defer server.Close()
		keypair, _ := core.NewDesktopKeypair()
		signals := make(chan EntitlementSignal, 1)
		dialer := NewDialer(DialerOptions{
			RelayURL: strings.Replace(server.URL, "http://", "ws://", 1), SessionID: "test-session-id-0123456789",
			Keypair: keypair, Forwarder: NewForwarder("http://127.0.0.1:1", http.DefaultClient), EntitlementSignal: func(signal EntitlementSignal) { signals <- signal },
		})
		ctx, cancel := context.WithCancel(context.Background())
		go dialer.Run(ctx)
		select {
		case signal := <-signals:
			cancel()
			if signal != EntitlementHandshakeRequired {
				t.Fatalf("signal=%s", signal)
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatal("missing handshake entitlement signal")
		}
	})

	t.Run("close 1008 entitlement expired", func(t *testing.T) {
		server := fakeRelay(t, func(conn *websocket.Conn) {
			_ = conn.Close(websocket.StatusPolicyViolation, "policy changed")
		})
		defer server.Close()
		keypair, _ := core.NewDesktopKeypair()
		signals := make(chan EntitlementSignal, 1)
		dialer := NewDialer(DialerOptions{
			RelayURL: strings.Replace(server.URL, "http://", "ws://", 1), SessionID: "test-session-id-0123456789",
			Keypair: keypair, Forwarder: NewForwarder("http://127.0.0.1:1", http.DefaultClient), EntitlementSignal: func(signal EntitlementSignal) { signals <- signal },
		})
		ctx, cancel := context.WithCancel(context.Background())
		go dialer.Run(ctx)
		select {
		case signal := <-signals:
			cancel()
			if signal != EntitlementExpired {
				t.Fatalf("signal=%s", signal)
			}
		case <-time.After(time.Second):
			cancel()
			t.Fatal("missing expiry entitlement signal")
		}
	})
}

// A relay that is down must not spin the desktop into a tight retry loop, and
// must not give up either.
func TestDialerRetriesAnUnavailableRelay(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unavailable.Close()

	keypair, _ := core.NewDesktopKeypair()
	dialer := NewDialer(DialerOptions{
		RelayURL:  strings.Replace(unavailable.URL, "http://", "ws://", 1),
		SessionID: "test-session-id-0123456789",
		Keypair:   keypair,
		Forwarder: NewForwarder("http://127.0.0.1:1", http.DefaultClient),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dialer.Run(ctx)

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got < 1 {
		t.Fatal("the dialer never tried to connect")
	}
	// With a 2s starting backoff, three seconds should allow a retry or two --
	// certainly not dozens, which would be the tight loop this guards against.
	if got > 10 {
		t.Fatalf("dialer retried %d times in 3s, which is a hot loop", got)
	}
}

// A relay that accepts a connection and immediately drops it -- flapping, being
// restarted, or actively hostile -- must not make the desktop hammer it.
//
// This is the failure the "unavailable relay" test above does not reach: that
// one is refused before the upgrade, so the dial fails and backs off. Here the
// dial SUCCEEDS and the read fails, which is a different branch. Treating that
// branch as an idle timeout retried with no delay produced twenty thousand
// dials in three seconds, which would bill the relay's owner for the privilege
// of being attacked.
func TestDialerBacksOffWhenTheRelayDropsAnAcceptedConnection(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	flapping := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.CloseNow()
	}))
	defer flapping.Close()

	keypair, _ := core.NewDesktopKeypair()
	dialer := NewDialer(DialerOptions{
		RelayURL:  strings.Replace(flapping.URL, "http://", "ws://", 1),
		SessionID: "test-session-id-0123456789",
		Keypair:   keypair,
		Forwarder: NewForwarder("http://127.0.0.1:1", http.DefaultClient),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dialer.Run(ctx)

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got > 10 {
		t.Fatalf("hot loop: %d connections in 3s against a relay that drops them", got)
	}
}

// A connection that worked for an hour and then dropped should retry promptly.
// Reusing the backoff from a previous outage would leave a healthy desktop
// unreachable for half a minute for no reason.
func TestBackoffResetsAfterAConnectionThatWorked(t *testing.T) {
	b := newBackoff()
	for i := 0; i < 8; i++ {
		b.next()
	}
	grown := b.next()
	b.reset()
	afterReset := b.next()

	if afterReset >= grown {
		t.Fatalf("backoff did not reset after a working connection: %v then %v", grown, afterReset)
	}
	if afterReset > 5*time.Second {
		t.Fatalf("first retry after a working connection should be prompt, got %v", afterReset)
	}
}

// A run's logs are routinely larger than a WebSocket library's default read
// limit, and the tunnel permits a 1 MiB encrypted payload. Without raising the
// limit on the connection the real ceiling was 32 KB, and exceeding it did not fail the
// request -- it tore down the socket, which then fed the reconnect loop.
func TestLargeFramesSurviveTheDialLoop(t *testing.T) {
	// A response comfortably past the 32 KB default but inside the ceiling.
	big := strings.Repeat("x", 200*1024)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(big))
	}))
	defer local.Close()

	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}

	done := make(chan error, 1)
	var once sync.Once
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		once.Do(func() {
			// The stand-in relay must not be the thing that truncates.
			conn.SetReadLimit(8 << 20)
			go func() {
				ctx := context.Background()
				phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
				if err != nil {
					done <- err
					return
				}
				first, err := phone.StartHandshake()
				if err != nil {
					done <- err
					return
				}
				channel := [relayChannelBytes]byte{8, 7, 6, 5, 4, 3, 2, 1}
				if err := conn.Write(ctx, websocket.MessageBinary, append(channel[:], first...)); err != nil {
					done <- err
					return
				}
				_, reply, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if len(reply) < relayChannelBytes || string(reply[:relayChannelBytes]) != string(channel[:]) {
					done <- errors.New("large-frame handshake reply used the wrong channel")
					return
				}
				if err := phone.FinishHandshake(reply[relayChannelBytes:]); err != nil {
					done <- err
					return
				}

				// A request body large enough to exceed the default limit in
				// the other direction too.
				body := []byte(strings.Repeat("y", 100*1024))
				request, err := EncodeRequestParts(http.MethodPost, "/v1/logs", nil, body)
				if err != nil {
					done <- err
					return
				}
				sealed, err := phone.Seal(request)
				if err != nil {
					done <- err
					return
				}
				if err := conn.Write(ctx, websocket.MessageBinary, append(channel[:], sealed...)); err != nil {
					done <- err
					return
				}
				_, frame, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if len(frame) < relayChannelBytes || string(frame[:relayChannelBytes]) != string(channel[:]) {
					done <- errors.New("large-frame response used the wrong channel")
					return
				}
				opened, err := phone.Open(frame[relayChannelBytes:])
				if err != nil {
					done <- err
					return
				}
				resp, err := DecodeResponse(opened)
				if err != nil {
					done <- err
					return
				}
				if resp.Status != http.StatusOK {
					done <- fmt.Errorf("status %d", resp.Status)
					return
				}
				if len(resp.Body) != len(big) {
					done <- fmt.Errorf("body truncated: got %d bytes, want %d", len(resp.Body), len(big))
					return
				}
				done <- nil
			}()
		})
	})
	defer relay.Close()

	dialer := NewDialer(DialerOptions{
		RelayURL:  strings.Replace(relay.URL, "http://", "ws://", 1),
		SessionID: "test-session-id-0123456789",
		Keypair:   keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go dialer.Run(ctx)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("large frame did not survive the tunnel: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out carrying a large frame")
	}
}

func TestExactHostWireBoundaryPreservesAnotherChannel(t *testing.T) {
	keypair := mustTestKeypair(t)
	done := make(chan error, 1)
	var once sync.Once
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		once.Do(func() {
			go func() {
				ctx := context.Background()
				// This is exactly a 1 MiB encrypted payload plus its eight-byte
				// host channel. It is deliberately invalid Noise for channel zero;
				// accepting and isolating it is proved by the valid channel below.
				boundary := make([]byte, maxHostWireFrame)
				if err := conn.Write(ctx, websocket.MessageBinary, boundary); err != nil {
					done <- err
					return
				}

				channel := relayChannel{9, 8, 7, 6, 5, 4, 3, 2}
				phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
				if err != nil {
					done <- err
					return
				}
				opening, err := phone.StartHandshake()
				if err != nil {
					done <- err
					return
				}
				if err := conn.Write(ctx, websocket.MessageBinary, append(channel[:], opening...)); err != nil {
					done <- err
					return
				}
				_, reply, err := conn.Read(ctx)
				if err != nil {
					done <- fmt.Errorf("boundary closed host socket: %w", err)
					return
				}
				if len(reply) < relayChannelBytes || string(reply[:relayChannelBytes]) != string(channel[:]) {
					done <- fmt.Errorf("reply crossed channel: %x", reply)
					return
				}
				done <- phone.FinishHandshake(reply[relayChannelBytes:])
			}()
		})
	})
	defer relay.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go NewDialer(DialerOptions{
		RelayURL: relay.URL, SessionID: "boundary-session-0123456789", Keypair: keypair,
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
	}).Run(ctx)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("timed out after exact host-wire boundary")
	}
}

// Entitlement tokens are bearer credentials. They travel in the WebSocket
// handshake header and must never appear in the URL infrastructure logs retain.
func TestEntitlementTokenUsesAHandshakeHeaderNotTheURL(t *testing.T) {
	token := "eyJleHAiOjE3ODg1MTM4MDN9.Pb8j33+KIif5vCjENO2yby9Q38q4=="
	dialer := NewDialer(DialerOptions{
		RelayURL:               "https://relay.example.com",
		SessionID:              "test-session-id-0123456789",
		EntitlementTokenSource: func() string { return token },
	})

	parsed, err := url.Parse(dialer.sessionURL())
	if err != nil {
		t.Fatalf("the dial URL does not parse: %v", err)
	}
	if got := parsed.Query().Get("entitlement"); got != "" {
		t.Fatalf("entitlement leaked into URL: %q", got)
	}
	if got := dialer.sessionHeaders().Get("X-Redline-Entitlement"); got != token {
		t.Fatalf("entitlement header = %q, want %q", got, token)
	}
	if got := parsed.Query().Get("role"); got != "host" {
		t.Fatalf("role: %q", got)
	}
	if parsed.Path != "/v1/session/test-session-id-0123456789" {
		t.Fatalf("path: %q", parsed.Path)
	}
}

// A dial failure must not carry the entitlement token in its message.
//
// Nothing logs this error today, which is exactly why it is worth fixing now:
// the leak is invisible until someone adds a log line, and then it is a
// credential in a file. The safe place to strip it is where it is created, not
// in every future caller.
func TestDialErrorsDoNotCarryTheEntitlementToken(t *testing.T) {
	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	const token = "eyJleHAiOjF9.c2lnbmF0dXJlLXZhbHVl"
	dialer := NewDialer(DialerOptions{
		// Port 1 is closed, so the dial fails immediately.
		RelayURL:               "http://127.0.0.1:1",
		SessionID:              "test-session-id-0123456789",
		EntitlementTokenSource: func() string { return token },
		Keypair:                keypair,
		Forwarder:              NewForwarder("http://127.0.0.1:1", http.DefaultClient),
	})

	dialErr := dialer.connect(context.Background())
	if dialErr == nil {
		t.Fatal("dialling a closed port should fail")
	}
	if strings.Contains(dialErr.Error(), token) {
		t.Fatalf("the entitlement token appears in a dial error:\n%v", dialErr)
	}
	// The error still has to be useful for diagnosis.
	if !strings.Contains(dialErr.Error(), "relay") {
		t.Fatalf("the error no longer says what failed: %v", dialErr)
	}
}

// A session id is generated, but a hostile or corrupted config value must not
// be able to add query parameters or climb the path.
func TestSessionIDCannotInjectIntoTheDialURL(t *testing.T) {
	dialer := NewDialer(DialerOptions{
		RelayURL:  "https://relay.example.com",
		SessionID: "evil?role=client&x=/../../admin",
	})

	parsed, err := url.Parse(dialer.sessionURL())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := parsed.Query().Get("role"); got != "host" {
		t.Fatalf("the session id overrode the role: %q", got)
	}
	if strings.Contains(parsed.Path, "..") {
		t.Fatalf("the session id climbed the path: %q", parsed.Path)
	}
}

// Cancelling the context must stop the loop, or shutting the service down
// would hang.
func TestDialerStopsOnContextCancel(t *testing.T) {
	keypair, _ := core.NewDesktopKeypair()
	dialer := NewDialer(DialerOptions{
		RelayURL:  "ws://127.0.0.1:1",
		SessionID: "test-session-id-0123456789",
		Keypair:   keypair,
		Forwarder: NewForwarder("http://127.0.0.1:1", http.DefaultClient),
	})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		dialer.Run(ctx)
		close(stopped)
	}()

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the dialer did not stop when its context was cancelled")
	}
}

type contextError struct {
	message string
	status  int
	body    string
}

func (e contextError) Error() string {
	return e.message + ": status=" + http.StatusText(e.status) + " body=" + e.body
}

func errContext(message string, status int, body string) error {
	return contextError{message: message, status: status, body: body}
}

// The dial loop was completely silent: no line on connect, failure, or
// backoff. A deployed relay that was working looked identical to one that was
// refusing every connection, and the only way to tell them apart was to open a
// second session and see whether the relay answered 409. An operator should
// not have to do that.
//
// These tests pin the observable behaviour rather than exact wording: that
// something is reported, that it reaches the caller's own sink, and that a
// credential never appears in it.

// recordingLog collects lines for assertions.
type recordingLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingLog) log(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recordingLog) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func TestDialerReportsWhenItConnects(t *testing.T) {
	rec := &recordingLog{}
	connected := make(chan struct{})
	var once sync.Once
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		once.Do(func() { close(connected) })
		<-time.After(2 * time.Second)
		conn.CloseNow()
	})
	defer relay.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dialer := NewDialer(DialerOptions{
		RelayURL:  relay.URL,
		SessionID: "test-session-1234",
		Keypair:   mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
		Logf:      rec.log,
	})
	go dialer.Run(ctx)

	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("relay never saw a connection")
	}
	// Give the dialer a moment to write its line.
	time.Sleep(200 * time.Millisecond)
	cancel()

	if got := rec.all(); !strings.Contains(strings.ToLower(got), "connect") {
		t.Errorf("connecting was not reported; log was:\n%s", got)
	}
}

func TestDialerReportsAFailureAndItsRetry(t *testing.T) {
	rec := &recordingLog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port 1 refuses immediately, so this is a dial failure rather than a
	// dropped session.
	dialer := NewDialer(DialerOptions{
		RelayURL:  "http://127.0.0.1:1",
		SessionID: "test-session-1234",
		Keypair:   mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
		Logf:      rec.log,
	})
	go dialer.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(strings.ToLower(rec.all()), "retry") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("a failure and its retry were never reported; log was:\n%s", rec.all())
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()

	got := strings.ToLower(rec.all())
	if !strings.Contains(got, "relay") {
		t.Errorf("the failure did not mention the relay; log was:\n%s", rec.all())
	}
}

func TestDialerNeverLogsTheEntitlementToken(t *testing.T) {
	rec := &recordingLog{}
	const secret = "eyJleHAiOjE4MjAwMzc4ODB9.c2VjcmV0LXNpZ25hdHVyZQ=="

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dialer := NewDialer(DialerOptions{
		RelayURL:               "http://127.0.0.1:1",
		SessionID:              "test-session-1234",
		Keypair:                mustTestKeypair(t),
		Forwarder:              NewForwarder("http://127.0.0.1:1", nil),
		EntitlementTokenSource: func() string { return secret },
		Logf:                   rec.log,
	})
	go dialer.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(strings.ToLower(rec.all()), "retry") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("nothing was logged; log was:\n%s", rec.all())
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()

	got := rec.all()
	if strings.Contains(got, secret) {
		t.Errorf("the entitlement token appeared in the log:\n%s", got)
	}
	// The signature half alone is just as bad.
	if strings.Contains(got, "c2VjcmV0LXNpZ25hdHVyZQ==") {
		t.Errorf("part of the entitlement token appeared in the log:\n%s", got)
	}
}

func TestDialerWithNoLoggerDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	// Logf left nil: every existing caller constructs DialerOptions without it.
	dialer := NewDialer(DialerOptions{
		RelayURL:  "http://127.0.0.1:1",
		SessionID: "test-session-1234",
		Keypair:   mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
	})
	dialer.Run(ctx)
}

// mustTestKeypair builds a desktop Noise identity for these tests.
func mustTestKeypair(t *testing.T) noise.DHKey {
	t.Helper()
	kp, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	return kp
}

// A malformed host envelope has no trustworthy channel id, so it reconnects
// the host with outage backoff rather than letting an untrusted relay create a
// hot loop. The next healthy connection must still recover.
func TestDialerBacksOffAndRecoversAfterMalformedHostEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{name: "short", frame: []byte("short")},
		{name: "oversize", frame: make([]byte, maxHostWireFrame+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dials := make(chan time.Time, 2)
			var attempts atomic.Int32
			relay := fakeRelay(t, func(conn *websocket.Conn) {
				dials <- time.Now()
				if attempts.Add(1) == 1 {
					_ = conn.Write(context.Background(), websocket.MessageBinary, tc.frame)
					conn.CloseNow()
					return
				}
				_, _, _ = conn.Read(context.Background())
			})
			defer relay.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dialer := NewDialer(DialerOptions{
				RelayURL: relay.URL, SessionID: "test-session-1234", Keypair: mustTestKeypair(t),
				Forwarder: NewForwarder("http://127.0.0.1:1", nil),
			})
			go dialer.Run(ctx)

			first := <-dials
			select {
			case second := <-dials:
				if elapsed := second.Sub(first); elapsed < time.Second {
					t.Fatalf("malformed envelope caused hot reconnect after %v", elapsed)
				}
				cancel()
			case <-ctx.Done():
				t.Fatal("dialer did not recover after malformed envelope backoff")
			}
		})
	}
}

func TestClosedChannelResponseWaitingForHostWriterIsDiscarded(t *testing.T) {
	keypair := mustTestKeypair(t)
	responses := make(chan []byte, 4)
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder("http://127.0.0.1:1", nil), func(ctx context.Context, frame []byte) error {
		if ctx.Err() != nil {
			t.Errorf("shared host writer received canceled channel context: %v", ctx.Err())
		}
		responses <- append([]byte(nil), frame...)
		return nil
	})
	defer mux.Close()

	channelA := relayChannel{1}
	phoneA, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	openingA, _ := phoneA.StartHandshake()

	// Hold the host writer so A's worker reaches the serialized write boundary.
	// Closing A while it waits deterministically reproduces the stale response
	// race without relying on scheduler timing.
	mux.writeMu.Lock()
	if err := mux.Dispatch(append(channelA[:], openingA...)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		mux.mu.Lock()
		entry := mux.sessions[channelA]
		processing := entry != nil && entry.processing
		mux.mu.Unlock()
		if processing {
			break
		}
		if time.Now().After(deadline) {
			mux.writeMu.Unlock()
			t.Fatal("channel A did not reach the host writer")
		}
		time.Sleep(time.Millisecond)
	}
	if err := mux.Dispatch(channelA[:]); err != nil {
		mux.writeMu.Unlock()
		t.Fatal(err)
	}

	// B and C both become ready while the same writer is held, exercising
	// contention between live channels after A has been canceled.
	type pendingPhone struct {
		channel relayChannel
		phone   *core.NoiseSession
	}
	pending := []pendingPhone{{channel: relayChannel{2}}, {channel: relayChannel{3}}}
	for i := range pending {
		phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
		opening, _ := phone.StartHandshake()
		pending[i].phone = phone
		if err := mux.Dispatch(append(pending[i].channel[:], opening...)); err != nil {
			mux.writeMu.Unlock()
			t.Fatal(err)
		}
	}
	mux.writeMu.Unlock()

	for range pending {
		select {
		case response := <-responses:
			if response[0] == channelA[0] {
				t.Fatal("stale channel A response reached the shared host writer")
			}
			var matched *pendingPhone
			for i := range pending {
				if response[0] == pending[i].channel[0] {
					matched = &pending[i]
					break
				}
			}
			if matched == nil {
				t.Fatalf("response used unknown channel %x", response[:relayChannelBytes])
			}
			if err := matched.phone.FinishHandshake(response[relayChannelBytes:]); err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("live channel write was blocked by canceled channel A")
		}
	}

	request, _ := EncodeRequestParts(http.MethodGet, "/still-live", nil, nil)
	sealed, _ := pending[0].phone.Seal(request)
	if err := mux.Dispatch(append(pending[0].channel[:], sealed...)); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responses:
		if response[0] != pending[0].channel[0] {
			t.Fatalf("operational response crossed channel: %x", response[:relayChannelBytes])
		}
		opened, err := pending[0].phone.Open(response[relayChannelBytes:])
		if err != nil {
			t.Fatalf("channel B lost Noise synchronization: %v", err)
		}
		decoded, err := DecodeResponse(opened)
		if err != nil || decoded.Status != http.StatusBadGateway {
			t.Fatalf("channel B response=%#v err=%v", decoded, err)
		}
	case <-time.After(time.Second):
		t.Fatal("channel B stopped operating after channel A cancellation")
	}
	select {
	case response := <-responses:
		t.Fatalf("unexpected stale response after live writes: %x", response[:relayChannelBytes])
	case <-time.After(20 * time.Millisecond):
	}
}

func TestMultiplexerRejectsOversizedResponseBeforeSharedWriter(t *testing.T) {
	keypair := mustTestKeypair(t)
	var writes atomic.Int32
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder("http://127.0.0.1:1", nil), func(context.Context, []byte) error {
		writes.Add(1)
		return nil
	})
	defer mux.Close()
	channel := relayChannel{1}
	entry := &multiplexedSession{}
	if err := mux.writeResponse(channel, entry, make([]byte, maxTunnelPayload+1)); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("oversized response error=%v", err)
	}
	if writes.Load() != 0 {
		t.Fatal("oversized response reached shared host writer")
	}
}

func TestChannelCloseAllowsFreshNoiseOnTheSameHostConnection(t *testing.T) {
	keypair := mustTestKeypair(t)
	responses := make(chan []byte, 2)
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder("http://127.0.0.1:1", nil), func(_ context.Context, frame []byte) error {
		responses <- append([]byte(nil), frame...)
		return nil
	})
	defer mux.Close()
	channel := relayChannel{1, 2, 3, 4, 5, 6, 7, 8}

	first, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	firstMsg, _ := first.StartHandshake()
	if err := mux.Dispatch(append(channel[:], firstMsg...)); err != nil {
		t.Fatal(err)
	}
	firstReply := <-responses
	if err := first.FinishHandshake(firstReply[relayChannelBytes:]); err != nil {
		t.Fatalf("first finish: %v", err)
	}

	// The relay's exact eight-byte notification deletes only this channel.
	if err := mux.Dispatch(channel[:]); err != nil {
		t.Fatal(err)
	}
	if mux.count() != 0 {
		t.Fatalf("closed channel remains in map: %d", mux.count())
	}
	// Unknown close is idempotent.
	if err := mux.Dispatch(channel[:]); err != nil {
		t.Fatalf("duplicate close: %v", err)
	}

	second, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	secondMsg, _ := second.StartHandshake()
	if err := mux.Dispatch(append(channel[:], secondMsg...)); err != nil {
		t.Fatal(err)
	}
	secondReply := <-responses
	if err := second.FinishHandshake(secondReply[relayChannelBytes:]); err != nil {
		t.Fatalf("fresh session after close: %v", err)
	}
}

func TestDialerReconnectReadsLatestTokenSource(t *testing.T) {
	var token atomic.Value
	token.Store("old-token")
	handshakes := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes <- r.Header.Get("X-Redline-Entitlement")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Write(context.Background(), websocket.MessageBinary, []byte("bad"))
		time.Sleep(20 * time.Millisecond)
		conn.CloseNow()
	}))
	defer server.Close()
	dialer := NewDialer(DialerOptions{
		RelayURL: server.URL, SessionID: "session-token-source-1234", Keypair: mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil), EntitlementTokenSource: func() string { return token.Load().(string) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.Run(ctx)
	if got := <-handshakes; got != "old-token" {
		t.Fatalf("first handshake token=%q", got)
	}
	token.Store("new-token")
	select {
	case got := <-handshakes:
		if got != "new-token" {
			t.Fatalf("reconnect token=%q want newest token", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("forced reconnect did not occur after bounded backoff")
	}
}

func TestDialerRejectsRedirectWithoutForwardingEntitlement(t *testing.T) {
	const secret = "websocket-redirect-secret"
	arrived := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		arrived <- r.Header.Get("X-Redline-Entitlement")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	dialer := NewDialer(DialerOptions{
		RelayURL: source.URL, SessionID: "session-no-redirect-1234", Keypair: mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil), EntitlementTokenSource: func() string { return secret },
	})
	if err := dialer.connect(context.Background()); err == nil {
		t.Fatal("redirecting WebSocket handshake was accepted")
	}
	select {
	case got := <-arrived:
		t.Fatalf("redirect target received credential %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	if dialer.handshakeClient().Timeout != 15*time.Second {
		t.Fatalf("handshake timeout=%v", dialer.handshakeClient().Timeout)
	}
}

func TestDialerRejectsHostileSessionBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, sessionID := range []string{"../admin-session-1234", "slash/session-1234", "dot%2Fescape-session", ".", "short"} {
		dialer := NewDialer(DialerOptions{RelayURL: server.URL, SessionID: sessionID})
		if err := dialer.connect(context.Background()); err == nil {
			t.Fatalf("session %q was accepted", sessionID)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid sessions reached network: %d", requests.Load())
	}
}

func TestMultiplexerIdleEvictionIsPerChannelAndGenerationSafe(t *testing.T) {
	keypair := mustTestKeypair(t)
	responses := make(chan []byte, 2)
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder("http://127.0.0.1:1", nil), func(_ context.Context, frame []byte) error {
		responses <- frame
		return nil
	})
	defer mux.Close()
	base := time.Unix(1_700_000_000, 0)
	current := base
	mux.now = func() time.Time { return current }

	channels := []relayChannel{{1}, {2}}
	for _, channel := range channels {
		phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
		opening, _ := phone.StartHandshake()
		if err := mux.Dispatch(append(channel[:], opening...)); err != nil {
			t.Fatal(err)
		}
		<-responses
	}

	mux.mu.Lock()
	first := mux.sessions[channels[0]]
	second := mux.sessions[channels[1]]
	first.lastSeen = base
	second.lastSeen = base.Add(idleTimeout - time.Second)
	mux.mu.Unlock()
	current = base.Add(idleTimeout)
	mux.evictIfIdle(channels[0], first)
	mux.evictIfIdle(channels[1], second)
	if mux.count() != 1 || !mux.active(channels[1], second) {
		t.Fatalf("idle eviction affected the active channel; count=%d", mux.count())
	}

	// Reuse the evicted bytes, then simulate the old timer firing late. The
	// pointer identity check must preserve the replacement generation.
	phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	opening, _ := phone.StartHandshake()
	if err := mux.Dispatch(append(channels[0][:], opening...)); err != nil {
		t.Fatal(err)
	}
	<-responses
	mux.evictIfIdle(channels[0], first)
	mux.mu.Lock()
	replacement := mux.sessions[channels[0]]
	mux.mu.Unlock()
	if replacement == nil || replacement == first {
		t.Fatal("late idle callback deleted the replacement generation")
	}
}

func TestMultiplexerBoundsChannelsAndHostFrames(t *testing.T) {
	keypair := mustTestKeypair(t)
	responses := make(chan []byte, maxRelayChannels)
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder("http://127.0.0.1:1", nil), func(_ context.Context, frame []byte) error {
		responses <- frame
		return nil
	})
	defer mux.Close()

	for i := 0; i < maxRelayChannels; i++ {
		channel := relayChannel{byte(i + 1)}
		phone, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
		opening, _ := phone.StartHandshake()
		if err := mux.Dispatch(append(channel[:], opening...)); err != nil {
			t.Fatal(err)
		}
	}
	if mux.count() != maxRelayChannels {
		t.Fatalf("channel count=%d want %d", mux.count(), maxRelayChannels)
	}
	extra := relayChannel{0xff}
	if err := mux.Dispatch(append(extra[:], byte(1))); err != nil {
		t.Fatalf("excess channel should be dropped safely: %v", err)
	}
	if mux.count() != maxRelayChannels {
		t.Fatalf("relay cap violation grew map to %d", mux.count())
	}

	boundary := make([]byte, maxHostWireFrame)
	copy(boundary, extra[:])
	if err := mux.Dispatch(boundary); err != nil {
		t.Fatalf("exact payload+channel boundary was refused: %v", err)
	}
	if err := mux.Dispatch(make([]byte, relayChannelBytes-1)); err == nil {
		t.Fatal("short host frame was accepted")
	}
	if err := mux.Dispatch(make([]byte, maxHostWireFrame+1)); err == nil {
		t.Fatal("oversize host frame was accepted")
	}
}

func TestInvalidChannelDoesNotDesynchronizeAnotherChannel(t *testing.T) {
	keypair := mustTestKeypair(t)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("still-working"))
	}))
	defer local.Close()
	responses := make(chan []byte, 4)
	mux := newSessionMultiplexer(context.Background(), keypair, NewForwarder(local.URL, local.Client()), func(_ context.Context, frame []byte) error {
		responses <- frame
		return nil
	})
	defer mux.Close()
	badChannel := relayChannel{1}
	goodChannel := relayChannel{2}

	if err := mux.Dispatch(append(badChannel[:], []byte("not noise")...)); err != nil {
		t.Fatal(err)
	}
	good, _ := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
	opening, _ := good.StartHandshake()
	if err := mux.Dispatch(append(goodChannel[:], opening...)); err != nil {
		t.Fatal(err)
	}
	handshake := <-responses
	if string(handshake[:relayChannelBytes]) != string(goodChannel[:]) {
		t.Fatalf("bad channel produced or stole the good response: %x", handshake[:relayChannelBytes])
	}
	if err := good.FinishHandshake(handshake[relayChannelBytes:]); err != nil {
		t.Fatal(err)
	}
	request, _ := EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
	sealed, _ := good.Seal(request)
	if err := mux.Dispatch(append(goodChannel[:], sealed...)); err != nil {
		t.Fatal(err)
	}
	answer := <-responses
	opened, err := good.Open(answer[relayChannelBytes:])
	if err != nil {
		t.Fatalf("good channel nonce state was corrupted: %v", err)
	}
	resp, err := DecodeResponse(opened)
	if err != nil || string(resp.Body) != "still-working" {
		t.Fatalf("good channel response=%q err=%v", resp.Body, err)
	}
}

type multiplexRelayClient struct {
	conn       *websocket.Conn
	channel    relayChannel
	generation uint64
	writeMu    sync.Mutex
}

type multiplexRelayHost struct {
	conn       *websocket.Conn
	generation uint64
}

// multiplexRelayDouble implements the Phase 1 channel wire contract without
// interpreting payloads. It gives integration tests two real mobile/core
// clients and the real desktop Dialer while keeping Cloudflare out of the test.
type multiplexRelayDouble struct {
	server     *httptest.Server
	mu         sync.Mutex
	host       *multiplexRelayHost
	clients    map[relayChannel]*multiplexRelayClient
	next       uint64
	generation uint64
	hostWrite  sync.Mutex
	hostEvents chan uint64
}

func newMultiplexRelayDouble() *multiplexRelayDouble {
	r := &multiplexRelayDouble{clients: make(map[relayChannel]*multiplexRelayClient), hostEvents: make(chan uint64, 8)}
	r.server = httptest.NewServer(http.HandlerFunc(r.serveHTTP))
	return r
}

func (r *multiplexRelayDouble) close() { r.server.Close() }

func (r *multiplexRelayDouble) serveHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Query().Get("role") {
	case "host":
		r.serveHost(w, req)
	case "client":
		r.serveClient(w, req)
	default:
		http.Error(w, "role", http.StatusBadRequest)
	}
}

func (r *multiplexRelayDouble) serveHost(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxHostWireFrame)
	r.mu.Lock()
	r.generation++
	host := &multiplexRelayHost{conn: conn, generation: r.generation}
	r.host = host
	r.mu.Unlock()
	r.hostEvents <- host.generation
	defer func() {
		r.mu.Lock()
		if r.host == host {
			r.host = nil
		}
		var clients []*multiplexRelayClient
		for channel, client := range r.clients {
			if client.generation == host.generation {
				delete(r.clients, channel)
				clients = append(clients, client)
			}
		}
		r.mu.Unlock()
		for _, client := range clients {
			_ = client.conn.Close(websocket.StatusNormalClosure, "peer disconnected")
		}
		conn.CloseNow()
	}()

	for {
		_, frame, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if len(frame) < relayChannelBytes {
			return
		}
		var channel relayChannel
		copy(channel[:], frame)
		r.mu.Lock()
		client := r.clients[channel]
		r.mu.Unlock()
		if client == nil || client.generation != host.generation {
			continue
		}
		client.writeMu.Lock()
		err = client.conn.Write(context.Background(), websocket.MessageBinary, frame[relayChannelBytes:])
		client.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (r *multiplexRelayDouble) serveClient(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	host := r.host
	if host == nil {
		r.mu.Unlock()
		http.Error(w, "no host", http.StatusLocked)
		return
	}
	r.next++
	channel := relayChannel{byte(r.next), byte(r.next >> 8), byte(r.next >> 16), byte(r.next >> 24)}
	generation := host.generation
	r.mu.Unlock()

	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxTunnelPayload)
	client := &multiplexRelayClient{conn: conn, channel: channel, generation: generation}
	r.mu.Lock()
	r.clients[channel] = client
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.clients[channel] == client {
			delete(r.clients, channel)
		}
		current := r.host
		r.mu.Unlock()
		if current != nil && current.generation == generation {
			r.hostWrite.Lock()
			_ = current.conn.Write(context.Background(), websocket.MessageBinary, channel[:])
			r.hostWrite.Unlock()
		}
		conn.CloseNow()
	}()

	for {
		_, payload, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		wire := make([]byte, relayChannelBytes+len(payload))
		copy(wire, channel[:])
		copy(wire[relayChannelBytes:], payload)
		r.mu.Lock()
		current := r.host
		r.mu.Unlock()
		if current == nil || current.generation != generation {
			return
		}
		r.hostWrite.Lock()
		err = current.conn.Write(context.Background(), websocket.MessageBinary, wire)
		r.hostWrite.Unlock()
		if err != nil {
			return
		}
	}
}

func (r *multiplexRelayDouble) closeHost() {
	r.mu.Lock()
	host := r.host
	r.mu.Unlock()
	if host != nil {
		host.conn.CloseNow()
	}
}

func waitRelayHost(t *testing.T, events <-chan uint64) uint64 {
	t.Helper()
	select {
	case generation := <-events:
		return generation
	case <-time.After(5 * time.Second):
		t.Fatal("desktop did not attach to relay")
		return 0
	}
}

func dialTestPhone(t *testing.T, relayURL, sessionID string, keypair noise.DHKey) *core.RelayClient {
	t.Helper()
	phone, err := core.DialRelay(relayURL, sessionID, core.DesktopPublicKey(keypair))
	if err != nil {
		t.Fatalf("dial phone: %v", err)
	}
	return phone
}

func TestTwoMobileClientsMultiplexThroughOneDesktop(t *testing.T) {
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/slow" {
			close(slowStarted)
			<-releaseSlow
		}
		w.Header().Set("X-Relay-Test", req.URL.Path)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(req.URL.Path))
	}))
	defer local.Close()

	relay := newMultiplexRelayDouble()
	defer relay.close()
	keypair := mustTestKeypair(t)
	const sessionID = "multiplex-session-0123456789"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialer := NewDialer(DialerOptions{
		RelayURL: relay.server.URL, SessionID: sessionID, Keypair: keypair,
		Forwarder: NewForwarder(local.URL, local.Client()),
	})
	go dialer.Run(ctx)
	firstGeneration := waitRelayHost(t, relay.hostEvents)

	type phoneResult struct {
		phone *core.RelayClient
		err   error
	}
	phones := make(chan phoneResult, 2)
	for range 2 {
		go func() {
			phone, err := core.DialRelay(relay.server.URL, sessionID, core.DesktopPublicKey(keypair))
			phones <- phoneResult{phone: phone, err: err}
		}()
	}
	firstResult := <-phones
	secondResult := <-phones
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent phone pairing: first=%v second=%v", firstResult.err, secondResult.err)
	}
	first := firstResult.phone
	second := secondResult.phone
	defer first.Close()
	defer second.Close()

	slowResult := make(chan error, 1)
	go func() {
		answer, err := first.AnswerFull(http.MethodGet, "/slow", "")
		if err == nil && (!strings.Contains(answer, `"status":202`) || !strings.Contains(answer, `"X-Relay-Test"`) || !strings.Contains(answer, "L3Nsb3c=")) {
			err = fmt.Errorf("slow response lost status/header/body: %s", answer)
		}
		slowResult <- err
	}()
	<-slowStarted

	fastDone := make(chan error, 1)
	go func() {
		answer, err := second.Request(http.MethodGet, "/fast", "")
		if err == nil && answer != "/fast" {
			err = fmt.Errorf("fast response crossed channels: %q", answer)
		}
		fastDone <- err
	}()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow channel blocked an independent phone")
	}
	close(releaseSlow)
	if err := <-slowResult; err != nil {
		t.Fatal(err)
	}

	first.Close()
	time.Sleep(50 * time.Millisecond)
	if answer, err := second.Request(http.MethodGet, "/after-close", ""); err != nil || answer != "/after-close" {
		t.Fatalf("surviving phone after peer close: answer=%q err=%v", answer, err)
	}
	third := dialTestPhone(t, relay.server.URL, sessionID, keypair)
	if answer, err := third.Request(http.MethodGet, "/fresh-phone", ""); err != nil || answer != "/fresh-phone" {
		t.Fatalf("fresh phone on existing host: answer=%q err=%v", answer, err)
	}
	third.Close()
	second.Close()

	// A host transport reconnect discards every old channel. The same desktop
	// identity then authenticates a completely fresh phone/Noise state.
	relay.closeHost()
	if generation := waitRelayHost(t, relay.hostEvents); generation == firstGeneration {
		t.Fatal("relay host generation did not advance")
	}
	fresh := dialTestPhone(t, relay.server.URL, sessionID, keypair)
	defer fresh.Close()
	if answer, err := fresh.Request(http.MethodGet, "/after-reconnect", ""); err != nil || answer != "/after-reconnect" {
		t.Fatalf("fresh Noise after host reconnect: answer=%q err=%v", answer, err)
	}
}
