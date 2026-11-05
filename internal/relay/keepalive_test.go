package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A host socket whose network path vanished without a FIN is indistinguishable
// from an idle one to a reader: Read just blocks. In production that lasted
// 30-100 minutes, during which the relay kept the dead socket registered and
// phones handshook into silence. The keepalive is what notices.
//
// The stand-in relay accepts and then never reads, so it never answers a ping
// -- the same observable behaviour as a half-open TCP path.
func TestDialerReconnectsWhenTheRelayStopsAnsweringPings(t *testing.T) {
	var mu sync.Mutex
	var dials int
	release := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-release
	}))
	defer silent.Close()
	defer close(release)

	rec := &recordingLog{}
	dialer := NewDialer(DialerOptions{
		RelayURL:          silent.URL,
		SessionID:         "test-session-keepalive-01",
		Keypair:           mustTestKeypair(t),
		Forwarder:         NewForwarder("http://127.0.0.1:1", nil),
		Logf:              rec.log,
		KeepaliveInterval: 100 * time.Millisecond,
		KeepaliveTimeout:  100 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := dials
		mu.Unlock()
		if got >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("dialer never noticed the unresponsive relay; dials=%d log:\n%s", got, rec.all())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !strings.Contains(rec.all(), "keepalive") {
		t.Errorf("the dead connection was not reported as a keepalive failure; log was:\n%s", rec.all())
	}
}

// A relay that answers pings is healthy no matter how long it carries no data.
// The keepalive must not tear down a quiet, working connection.
func TestKeepaliveLeavesAResponsiveIdleConnectionAlone(t *testing.T) {
	var mu sync.Mutex
	var dials int
	responsive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// Reading is what answers pings in coder/websocket.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer responsive.Close()

	rec := &recordingLog{}
	dialer := NewDialer(DialerOptions{
		RelayURL:          responsive.URL,
		SessionID:         "test-session-keepalive-02",
		Keypair:           mustTestKeypair(t),
		Forwarder:         NewForwarder("http://127.0.0.1:1", nil),
		Logf:              rec.log,
		KeepaliveInterval: 50 * time.Millisecond,
		KeepaliveTimeout:  500 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	dialer.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if dials != 1 {
		t.Fatalf("a responsive idle relay was redialled: dials=%d log:\n%s", dials, rec.all())
	}
}

// The relay closes a displaced host with 4409. Saying so in the log is what
// lets an operator spot two desktops sharing one session id, which otherwise
// looks like an unexplained flapping connection.
func TestDialerReportsWhenAnotherHostReplacedIt(t *testing.T) {
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		_ = conn.Close(websocket.StatusCode(4409), "replaced by a newer host connection")
	})
	defer relay.Close()

	rec := &recordingLog{}
	dialer := NewDialer(DialerOptions{
		RelayURL:  relay.URL,
		SessionID: "test-session-replaced-01",
		Keypair:   mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
		Logf:      rec.log,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.Run(ctx)

	deadline := time.After(5 * time.Second)
	for !strings.Contains(rec.all(), "another desktop") {
		select {
		case <-deadline:
			t.Fatalf("replacement was not reported; log was:\n%s", rec.all())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestKeepaliveDefaultsAreBoundedAndNonZero(t *testing.T) {
	d := NewDialer(DialerOptions{})
	interval, timeout := d.keepalive()
	if interval <= 0 || interval > time.Minute {
		t.Fatalf("default keepalive interval %v must be positive and at most a minute", interval)
	}
	if timeout <= 0 || timeout >= interval*2 {
		t.Fatalf("default keepalive timeout %v must be positive and short relative to the interval %v", timeout, interval)
	}
}
