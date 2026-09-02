package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recorder captures what the stream hands back, standing in for the UI.
type recorder struct {
	mu       sync.Mutex
	payloads []string
	states   []string
}

func (r *recorder) OnUsage(payload string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, payload)
}

func (r *recorder) OnState(state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
}

func (r *recorder) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.payloads...), append([]string(nil), r.states...)
}

func (r *recorder) waitForPayloads(t *testing.T, count int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		payloads, _ := r.snapshot()
		if len(payloads) >= count {
			return payloads
		}
		time.Sleep(10 * time.Millisecond)
	}
	payloads, states := r.snapshot()
	t.Fatalf("timed out waiting for %d payloads; got %d, states %v", count, len(payloads), states)
	return nil
}

func (r *recorder) waitForState(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, states := r.snapshot()
		for _, state := range states {
			if state == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, states := r.snapshot()
	t.Fatalf("never saw state %q; saw %v", want, states)
}

// usageFrame builds one SSE data payload.
//
// Deliberately a single line: SSE delimits fields by newline, so a payload
// containing a literal newline would be read as the end of the frame and the
// rest as a new field. Wrapping this JSON for readability silently breaks the
// protocol, which is a mistake worth not repeating.
func usageFrame(now time.Time, remaining float64) string {
	return fmt.Sprintf(
		`{"generated_at":%q,"providers":[{"id":"claude-main","provider":"claude",`+
			`"usage_source":{"active":"openusage"},"active_runs":0,"max_concurrent_runs":1,`+
			`"snapshot":{"provider":"claude","observed_at":%q,"source":"openusage",`+
			`"weekly":{"remaining":%v,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339), remaining,
		now.Add(48*time.Hour).Format(time.RFC3339))
}

// The stream must deliver the same rendered shape the polling path produces, so
// the UI has one thing to render rather than two.
func TestStreamUsageDeliversRenderedViews(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The stream re-sends the whole read model repeatedly, so trimming is
		// where the saving compounds.
		if got := r.URL.Query().Get("fields"); got != "providers,health" {
			t.Errorf("fields = %q, want providers,health", got)
		}
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", usageFrame(now, 0.8))
		flusher.Flush()
		fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", usageFrame(now, 0.6))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	sink := &recorder{}
	stream := client.StreamUsage(sink)
	defer stream.Stop()

	payloads := sink.waitForPayloads(t, 2)

	var first UsageView
	if err := json.Unmarshal([]byte(payloads[0]), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.Providers[0].Weekly.RemainingPercent != 80 {
		t.Errorf("first frame = %d%%", first.Providers[0].Weekly.RemainingPercent)
	}

	var second UsageView
	_ = json.Unmarshal([]byte(payloads[1]), &second)
	if second.Providers[0].Weekly.RemainingPercent != 60 {
		t.Errorf("second frame = %d%%", second.Providers[0].Weekly.RemainingPercent)
	}
}

// A live connection is only useful if the UI can say whether it is live. The
// web dashboard shows exactly this as a pill.
func TestStreamUsageReportsConnectionState(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", usageFrame(now, 0.8))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	sink := &recorder{}
	stream := client.StreamUsage(sink)
	defer stream.Stop()

	sink.waitForState(t, StreamStateLive)
}

// A dropped desktop must not leave the phone showing numbers as though they
// were current, and it must not give up: the desktop coming back is the normal
// case, not an exception.
func TestStreamUsageReconnectsAfterADrop(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var connections int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connections++
		attempt := connections
		mu.Unlock()

		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", usageFrame(now, 0.8))
		flusher.Flush()
		if attempt == 1 {
			// Drop the first connection to force a reconnect.
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	sink := &recorder{}
	stream := client.StreamUsageWithBackoff(sink, 10)
	defer stream.Stop()

	// Two payloads means it reconnected and resumed delivering.
	sink.waitForPayloads(t, 2)
	sink.waitForState(t, StreamStateReconnecting)

	mu.Lock()
	defer mu.Unlock()
	if connections < 2 {
		t.Errorf("expected a reconnect, got %d connections", connections)
	}
}

// Stopping must actually stop: a stream that keeps reconnecting after the
// screen is gone would drain the battery for nothing.
func TestStreamStopEndsReconnection(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var connections int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connections++
		mu.Unlock()
		// Always drop, so an unstopped stream would reconnect forever.
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	stream := client.StreamUsageWithBackoff(&recorder{}, 10)

	time.Sleep(120 * time.Millisecond)
	// Stop blocks until the reconnect goroutine has exited, so anything that
	// arrives afterwards would be a leak rather than a race.
	stream.Stop()

	// A request already on the wire when Stop was called can still reach the
	// handler, so settle before taking the baseline. Measuring immediately
	// would count that in-flight request as a post-stop reconnect.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	afterStop := connections
	mu.Unlock()

	// The retry delay is 10ms, so this window would fit many reconnects if the
	// goroutine were still running.
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if connections > afterStop {
		t.Errorf("stream kept reconnecting after Stop: %d -> %d", afterStop, connections)
	}
}

// A rejected credential is not a transport hiccup: reconnecting forever would
// hammer the desktop and never recover, so the stream stops and says why.
func TestStreamUsageStopsOnUnauthorized(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var connections int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		connections++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "bad", fixedClock(now))
	sink := &recorder{}
	stream := client.StreamUsageWithBackoff(sink, 10)
	defer stream.Stop()

	sink.waitForState(t, StreamStateUnauthorized)

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if connections > 1 {
		t.Errorf("a rejected credential must not be retried, got %d attempts", connections)
	}
}

// A malformed frame must not kill the stream: one bad payload is not a reason
// to stop showing everything after it.
func TestStreamUsageSkipsMalformedFrames(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: dashboard\ndata: {not json\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", usageFrame(now, 0.7))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	sink := &recorder{}
	stream := client.StreamUsage(sink)
	defer stream.Stop()

	payloads := sink.waitForPayloads(t, 1)
	var view UsageView
	if err := json.Unmarshal([]byte(payloads[0]), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Providers[0].Weekly.RemainingPercent != 70 {
		t.Errorf("the good frame after a bad one must still arrive, got %d%%",
			view.Providers[0].Weekly.RemainingPercent)
	}
}
