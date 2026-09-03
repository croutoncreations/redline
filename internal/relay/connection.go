package relay

import (
	"math/rand/v2"
	"sync"
	"time"
)

// maxBackoff caps exponential backoff for relay reconnects.
//
// A desktop that is reachable but erroring should not be retried at a fixed
// rate forever: that drains the phone and hammers a service that is already
// unwell. The delay grows to this ceiling and stays there, so a relay that
// recovers is still picked up within half a minute.
const maxBackoff = 30 * time.Second

// idleTimeout is how long a relay session may be open with no traffic before
// the desktop closes it.
//
// Relayed minutes meter against a Cloudflare bill and a phone's battery. An
// always-open session with no activity is ~83% of the Durable Object free
// tier's daily CPU allowance consumed for nothing. Five minutes is long
// enough that a briefly backgrounded phone can come back, but short enough
// that a forgotten open session is not the expensive case.
const idleTimeout = 5 * time.Minute

// backoff tracks how long to wait before the next reconnect attempt.
//
// It grows exponentially from a short initial delay, capped at maxBackoff, and
// resets when a connection succeeds. Jitter is added to spread reconnect
// storms if many desktops lose the relay simultaneously.
type backoff struct {
	current time.Duration
}

// initialBackoff is the base for the first retry. Starting below 5 s keeps the
// first retry feeling responsive; starting above 1 s avoids hammering a relay
// that just dropped the connection.
const initialBackoff = 2 * time.Second

func newBackoff() *backoff {
	return &backoff{current: initialBackoff}
}

// next returns how long the caller should sleep before the next attempt and
// advances the internal state for the attempt after that.
//
// Jitter spreads reconnect storms: if ten desktops all lost the relay at the
// same moment, a synchronised retry at t+2s would hit it in a single burst.
// Adding ±25% randomness staggers them without materially changing the
// perceived reconnect time for any individual user.
func (b *backoff) next() time.Duration {
	d := b.current

	// Jitter: ±25% of the current delay, so the result stays within
	// [0.75×d, 1.25×d]. This must be computed before we advance b.current so
	// the cap check uses the base value.
	jitter := time.Duration(rand.Float64()*float64(d)/2) - d/4

	b.current *= 2
	if b.current > maxBackoff {
		b.current = maxBackoff
	}

	result := d + jitter
	if result < time.Second {
		result = time.Second
	}
	if result > maxBackoff {
		result = maxBackoff
	}
	return result
}

// reset returns the backoff to its initial state.
//
// Called when a connection succeeds: a desktop that connected once should
// retry quickly after the next drop, not at the cap it ground up to during
// the previous outage.
func (b *backoff) reset() {
	b.current = initialBackoff
}

// idleTracker records when traffic last crossed the relay tunnel.
//
// It is safe for concurrent use because a reader goroutine may call sawTraffic
// from the receive path while a ticker goroutine calls expired from the idle
// check. Both touch lastSeen, so they must coordinate.
type idleTracker struct {
	mu       sync.Mutex
	timeout  time.Duration
	lastSeen time.Time
}

func newIdleTracker(d time.Duration) *idleTracker {
	return &idleTracker{
		timeout:  d,
		lastSeen: time.Now(),
	}
}

// sawTraffic resets the idle clock. Calling it with a past time is correct in
// tests that control the clock.
func (t *idleTracker) sawTraffic(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if at.After(t.lastSeen) {
		t.lastSeen = at
	}
}

// expired reports whether the session has been idle long enough to close.
// Passing a future time is correct in tests that want to fast-forward the
// clock without sleeping.
func (t *idleTracker) expired(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return now.Sub(t.lastSeen) >= t.timeout
}
