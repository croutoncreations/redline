package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
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
				if err := conn.Write(ctx, websocket.MessageBinary, first); err != nil {
					done <- err
					return
				}
				_, reply, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if err := phone.FinishHandshake(reply); err != nil {
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
				if err := conn.Write(ctx, websocket.MessageBinary, sealed); err != nil {
					done <- err
					return
				}
				_, frame, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				opened, err := phone.Open(frame)
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

// An idle timeout is how a session normally ends: the phone went away. It must
// not be treated as a failure, because the accrued backoff would then make the
// desktop slow to answer the next time someone opened the app -- punishing the
// user for having put their phone down.
//
// The distinction has to be drawn from the real read deadline rather than from
// "any error after connecting", or every dropped socket becomes an instant
// retry.
func TestOnlyARealDeadlineCountsAsIdle(t *testing.T) {
	if !errors.Is(fmt.Errorf("%w after %v", errIdle, 5*time.Minute), errIdle) {
		t.Fatal("an idle timeout must be recognisable as such by the caller")
	}
	for _, notIdle := range []error{
		errors.New("connection refused"),
		io.EOF,
		context.Canceled,
	} {
		if errors.Is(notIdle, errIdle) {
			t.Fatalf("%v must not look like an idle timeout", notIdle)
		}
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
// limit, and the tunnel advertises a 4 MB ceiling. Without raising the limit on
// the connection the real ceiling was 32 KB, and exceeding it did not fail the
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
				if err := conn.Write(ctx, websocket.MessageBinary, first); err != nil {
					done <- err
					return
				}
				_, reply, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				if err := phone.FinishHandshake(reply); err != nil {
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
				if err := conn.Write(ctx, websocket.MessageBinary, sealed); err != nil {
					done <- err
					return
				}
				_, frame, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				opened, err := phone.Open(frame)
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

// Entitlement tokens are standard base64, whose alphabet includes '+' -- which
// a query string decodes as a space. Concatenating one unescaped would corrupt
// roughly half of all real tokens, and the symptom would be a paying customer
// told they had not paid.
func TestEntitlementTokenSurvivesTheQueryString(t *testing.T) {
	token := "eyJleHAiOjE3ODg1MTM4MDN9.Pb8j33+KIif5vCjENO2yby9Q38q4=="
	dialer := NewDialer(DialerOptions{
		RelayURL:         "https://relay.example.com",
		SessionID:        "test-session-id-0123456789",
		EntitlementToken: token,
	})

	parsed, err := url.Parse(dialer.sessionURL())
	if err != nil {
		t.Fatalf("the dial URL does not parse: %v", err)
	}
	if got := parsed.Query().Get("entitlement"); got != token {
		t.Fatalf("token was mangled in the URL:\n got %q\nwant %q", got, token)
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
		RelayURL:         "http://127.0.0.1:1",
		SessionID:        "test-session-id-0123456789",
		EntitlementToken: token,
		Keypair:          keypair,
		Forwarder:        NewForwarder("http://127.0.0.1:1", http.DefaultClient),
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
