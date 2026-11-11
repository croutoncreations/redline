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
	if !strings.Contains(rec.all(), errKeepaliveTimeout.Error()) {
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

// A failed ping is excused when data was being sent during it: either a write
// was still in flight (holding the shared write lock), or one finished after
// the ping started (its bytes were queued in the kernel send buffer ahead of
// the ping, so the pong was legitimately late). Neither excuse lasts past the
// stall bound, because a write into a vanished path also never finishes.
func TestKeepaliveForgivesAPingDelayedBySending(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	pingStarted := now.Add(-10 * time.Second)
	const limit = time.Minute
	for _, tc := range []struct {
		name     string
		activity writeActivity
		wantDead bool
	}{
		{"no writes at all", writeActivity{}, true},
		{"last write ended before the ping", writeActivity{lastEnd: pingStarted.Add(-time.Second)}, true},
		{"write in flight", writeActivity{inFlightSince: now.Add(-2 * time.Second)}, false},
		{"write ended during the ping (buffer draining)", writeActivity{lastStart: pingStarted.Add(-5 * time.Second), lastEnd: pingStarted.Add(time.Second)}, false},
		{"write in flight past the stall bound", writeActivity{inFlightSince: now.Add(-limit - time.Second)}, true},
		{"write ended during the ping but long stalled", writeActivity{lastEnd: pingStarted.Add(time.Second), lastStart: now.Add(-limit - time.Second)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pingFailureIsFatal(tc.activity, pingStarted, now, limit); got != tc.wantDead {
				t.Fatalf("pingFailureIsFatal = %v, want %v", got, tc.wantDead)
			}
		})
	}
}

// The wiring, not just the predicate: a relay that accepts a large frame and
// then stops reading wedges the desktop's write. Pings fail behind it, but the
// connection must not be torn down while the write is excused, and must be
// once the stall bound passes.
func TestKeepaliveExcusesAWedgedWriteOnlyUntilTheStallBound(t *testing.T) {
	var mu sync.Mutex
	var dials int
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		mu.Unlock()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		<-release // never reads: pings go unanswered and the send buffer fills
	}))
	defer relay.Close()
	defer close(release)

	writes := &writeTracker{}
	conn, _, err := websocket.Dial(context.Background(), relay.URL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Wedge a write: far more than any socket buffer, into a peer that never reads.
	go func() {
		writes.begin()
		_ = conn.Write(context.Background(), websocket.MessageBinary, make([]byte, 64<<20))
		writes.end()
	}()
	time.Sleep(100 * time.Millisecond)

	dialer := NewDialer(DialerOptions{KeepaliveInterval: 20 * time.Millisecond, KeepaliveTimeout: 50 * time.Millisecond})
	failed := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.runKeepaliveWithStall(ctx, conn, writes, 600*time.Millisecond, func() { close(failed) })

	select {
	case <-failed:
		t.Fatal("keepalive gave up on a connection whose write was still excused")
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-failed:
	case <-time.After(10 * time.Second):
		t.Fatal("keepalive never gave up after the stall bound")
	}
}

// A ping that fails because the socket was already closed is left to readLoop
// so the specific reason (4409, 1008, a write error) is what gets reported.
// Calls runKeepalive directly on a closed conn: through Run the race is too
// narrow to catch reliably.
func TestKeepaliveDoesNotClaimAClosedSocket(t *testing.T) {
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	defer relay.Close()
	conn, _, err := websocket.Dial(context.Background(), relay.URL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.CloseNow()

	dialer := NewDialer(DialerOptions{KeepaliveInterval: 5 * time.Millisecond, KeepaliveTimeout: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failedCalled := false
	dialer.runKeepalive(ctx, conn, &writeTracker{}, func() { failedCalled = true })
	if failedCalled {
		t.Fatal("a ping on an already-closed socket was reported as a keepalive timeout")
	}
}

// A ping that fails because the relay closed the socket is not a keepalive
// timeout. Reporting it as one would hide a 4409 replacement or a 1008
// entitlement close -- the lines an operator needs.
func TestKeepaliveIgnoresAPingFailedByAClosedSocket(t *testing.T) {
	relay := fakeRelay(t, func(conn *websocket.Conn) {
		// Keep reading so pings are answered and the close handshake can
		// complete, then close with 4409 while pings are in flight. (A read
		// whose context expires tears the socket down, so the close must come
		// from a separate goroutine rather than after a timed-out read.)
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = conn.Close(websocket.StatusCode(4409), "replaced by a newer host connection")
		}()
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	defer relay.Close()

	rec := &recordingLog{}
	dialer := NewDialer(DialerOptions{
		RelayURL:          relay.URL,
		SessionID:         "test-session-keepalive-03",
		Keypair:           mustTestKeypair(t),
		Forwarder:         NewForwarder("http://127.0.0.1:1", nil),
		Logf:              rec.log,
		KeepaliveInterval: 10 * time.Millisecond,
		KeepaliveTimeout:  time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go dialer.Run(ctx)

	deadline := time.After(5 * time.Second)
	for !strings.Contains(rec.all(), "another desktop") {
		select {
		case <-deadline:
			t.Fatalf("4409 was not reported as a replacement; log was:\n%s", rec.all())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if strings.Contains(rec.all(), errKeepaliveTimeout.Error()) {
		t.Fatalf("a relay-initiated close was misreported as a keepalive timeout; log was:\n%s", rec.all())
	}
}

// A connection that stayed up must not inherit the backoff an earlier outage
// grew to. In production every reconnect after a working connection waited
// ~23s on average (681 of 876 at 20s or more), stacked on top of detection.
// A connection that drops immediately must still back off, or a relay that
// accepts and instantly closes would be redialled in a hot loop.
func TestBackoffResetsOnlyAfterAConnectionThatLasted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lasted    time.Duration
		wantReset bool
	}{
		{"never connected", 0, false},
		{"dropped at once", 100 * time.Millisecond, false},
		{"stayed up", stableConnection + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackoff()
			for i := 0; i < 8; i++ {
				b.next()
			}
			grown := b.current
			resetIfStable(b, tc.lasted)
			if reset := b.current < grown; reset != tc.wantReset {
				t.Fatalf("reset=%v after a connection that lasted %v, want %v", reset, tc.lasted, tc.wantReset)
			}
		})
	}
}

// End to end through Run: after a connection that stayed up, the first retry
// uses the initial backoff even though earlier failures had grown it.
func TestDialerRetriesPromptlyAfterAStableConnectionDrops(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out real backoff and stability windows (~25s)")
	}
	var mu sync.Mutex
	var dials []time.Time
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials = append(dials, time.Now())
		n := len(dials)
		mu.Unlock()
		if n <= 3 {
			// Three refusals grow the backoff to ~8s.
			w.WriteHeader(http.StatusConflict)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if n == 4 {
			// Stay up past the stability threshold, then drop.
			go func() {
				time.Sleep(stableConnection + 200*time.Millisecond)
				conn.CloseNow()
			}()
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	defer relay.Close()

	dialer := NewDialer(DialerOptions{
		RelayURL:  relay.URL,
		SessionID: "test-session-backoff-reset-01",
		Keypair:   mustTestKeypair(t),
		Forwarder: NewForwarder("http://127.0.0.1:1", nil),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	go dialer.Run(ctx)

	for {
		mu.Lock()
		n := len(dials)
		var gap time.Duration
		if n >= 5 {
			gap = dials[4].Sub(dials[3])
		}
		mu.Unlock()
		if n >= 5 {
			// Fourth dial lasted stableConnection+0.2s; a reset retry adds at
			// most initialBackoff*1.25 = 2.5s. Without the reset the gap
			// measured 22s, so a 4s allowance keeps margin for loaded CI.
			if limit := stableConnection + 200*time.Millisecond + 4*time.Second; gap > limit {
				t.Fatalf("retry after a stable connection took %v, want under %v", gap, limit)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("only %d dials before timeout", n)
		case <-time.After(50 * time.Millisecond):
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
