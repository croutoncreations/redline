package relay

import (
	"testing"
	"time"
)

// A desktop that gives up after one failure is useless: laptops sleep, wifi
// drops, and the relay itself may restart. A desktop that retries in a tight
// loop is worse, because it drains battery and hammers a service that is
// probably already unwell.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	b := newBackoff()

	first := b.next()
	if first < time.Second || first > 5*time.Second {
		t.Fatalf("first retry should be quick but not instant, got %v", first)
	}

	var last time.Duration
	for i := 0; i < 20; i++ {
		last = b.next()
	}
	if last > maxBackoff {
		t.Fatalf("backoff grew past its cap: %v > %v", last, maxBackoff)
	}
	if last <= first {
		t.Fatalf("backoff did not grow: first %v, last %v", first, last)
	}
}

// A connection that succeeds and then drops an hour later should retry
// quickly, not at the cap it reached during the last outage.
func TestBackoffResetsAfterASuccessfulConnection(t *testing.T) {
	b := newBackoff()
	for i := 0; i < 10; i++ {
		b.next()
	}
	b.reset()
	if got := b.next(); got > 5*time.Second {
		t.Fatalf("backoff did not reset after success: %v", got)
	}
}

// Relayed minutes are metered against a Cloudflare bill and a phone's battery,
// so an idle session must not be held open forever. This is the guardrail that
// keeps a forgotten app in the foreground from being the expensive case.
func TestIdleSessionsAreClosed(t *testing.T) {
	if idleTimeout <= 0 {
		t.Fatal("an idle timeout must exist or a forgotten session runs forever")
	}
	if idleTimeout > 15*time.Minute {
		t.Fatalf("idle timeout of %v is too generous to control cost", idleTimeout)
	}

	tracker := newIdleTracker(idleTimeout)
	if tracker.expired(time.Now()) {
		t.Fatal("a fresh session must not be considered idle")
	}
	if !tracker.expired(time.Now().Add(idleTimeout + time.Second)) {
		t.Fatal("a session past the idle timeout must be closed")
	}

	// Any traffic keeps it alive.
	tracker.sawTraffic(time.Now().Add(idleTimeout - time.Second))
	if tracker.expired(time.Now().Add(idleTimeout + time.Second)) {
		t.Fatal("traffic should have reset the idle timer")
	}
}
