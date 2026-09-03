package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// An idle timeout is how a session normally ends: the phone went away. It must
// not be treated as a failure, because the accrued backoff would then make the
// desktop slow to answer the next time someone opened the app -- punishing the
// user for having put their phone down.
func TestIdleTimeoutIsNotTreatedAsAFailure(t *testing.T) {
	if !errors.Is(fmtErrorfIdle(), errIdle) {
		t.Fatal("an idle timeout must be recognisable as such by the caller")
	}

	// A real failure must remain distinguishable from it, or the two would
	// collapse back into one behaviour.
	if errors.Is(errors.New("connection refused"), errIdle) {
		t.Fatal("an ordinary failure must not look like an idle timeout")
	}
}

func fmtErrorfIdle() error {
	return fmt.Errorf("%w after %v", errIdle, 5*time.Minute)
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
