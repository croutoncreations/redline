package push

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/store"
)

type memoryStore struct {
	mu            sync.Mutex
	registrations []store.PushRegistration
	deleted       []string
}

func (s *memoryStore) ActivePushRegistrations(context.Context, string) ([]store.PushRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.PushRegistration(nil), s.registrations...), nil
}
func (s *memoryStore) ActivePushRegistration(_ context.Context, _, id string) (store.PushRegistration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.registrations {
		if r.DeviceSessionID == id {
			return r, true, nil
		}
	}
	return store.PushRegistration{}, false, nil
}
func (s *memoryStore) DeletePushRegistrationToken(_ context.Context, id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}
func TestRelayDispatchPrivacyAuthAndErrors(t *testing.T) {
	for _, status := range []int{200, 202, 410, 404, 501} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if r.URL.Path != "/v1/push" || r.Header.Get("Authorization") != "Bearer shared-secret" {
					t.Error("incorrect request path or auth")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 4 || body["title"] != nil || body["message"] != nil {
					t.Error("unexpected content fields")
				}
				var hint WakeHint
				_ = json.Unmarshal(body["hint"], &hint)
				if hint.DeliveryID != 42 || hint.Kind != "reset" {
					t.Errorf("hint=%+v", hint)
				}
				w.WriteHeader(status)
				// Hostile relay response must never be logged.
				_, _ = w.Write([]byte(`{"error":"shared-secret private-token"}`))
			}))
			defer relay.Close()
			data := &memoryStore{registrations: []store.PushRegistration{{DeviceSessionID: "phone", Provider: "example", Platform: "android", Token: "private-token"}}}
			var logs bytes.Buffer
			transport := &RelayTransport{BaseURL: relay.URL, AuthToken: "shared-secret"}
			d := NewDispatcher(data, "issuer", transport, log.New(&logs, "", 0))
			defer d.Close()
			result := d.Dispatch(t.Context(), 42, "reset.expiring", true)
			if result.Woken != map[int]int{200: 1, 202: 1, 410: 0, 404: 0, 501: 0}[status] {
				t.Fatalf("result=%+v", result)
			}
			if status == 501 || status == 404 {
				if result.Configured {
					t.Fatal("relay unavailable reported configured")
				}
				transport.mu.Lock()
				remaining := time.Until(transport.unavailableUntil)
				transport.mu.Unlock()
				if remaining < 59*time.Minute || remaining > time.Hour {
					t.Fatalf("backoff=%s", remaining)
				}
				d.Dispatch(t.Context(), 42, "reset.expiring", true)
				mu.Lock()
				n := calls
				mu.Unlock()
				if n != 1 {
					t.Fatalf("backoff calls=%d", n)
				}
				if strings.Count(logs.String(), "push not configured") != 1 {
					t.Fatal("unavailable log was not deduplicated")
				}
			}
			if status == 410 && len(data.deleted) != 1 {
				t.Fatal("registration not deleted")
			}
			if strings.Contains(logs.String(), "shared-secret") || strings.Contains(logs.String(), "private-token") {
				t.Fatal("sensitive data logged")
			}
		})
	}
}

// A relay outage logs once, but a fresh outage after recovery logs again so
// operators are not left blind for the rest of the process lifetime.
func TestRelayOutageLoggedOncePerOutage(t *testing.T) {
	var mu sync.Mutex
	status := 501
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
	}))
	defer relay.Close()
	setStatus := func(s int) { mu.Lock(); status = s; mu.Unlock() }
	data := &memoryStore{registrations: []store.PushRegistration{{DeviceSessionID: "phone", Provider: "example", Platform: "android", Token: "t"}}}
	var logs bytes.Buffer
	transport := &RelayTransport{BaseURL: relay.URL, AuthToken: "s"}
	d := NewDispatcher(data, "issuer", transport, log.New(&logs, "", 0))
	defer d.Close()
	expire := func() { transport.mu.Lock(); transport.unavailableUntil = time.Time{}; transport.mu.Unlock() }
	d.Dispatch(t.Context(), 1, "reset.announced", true)
	d.Dispatch(t.Context(), 2, "reset.announced", true) // still backing off
	expire()
	d.Dispatch(t.Context(), 3, "reset.announced", true) // same outage persists
	if n := strings.Count(logs.String(), "push not configured"); n != 1 {
		t.Fatalf("one outage logged %d times", n)
	}
	setStatus(200)
	expire()
	if r := d.Dispatch(t.Context(), 4, "reset.announced", true); r.Woken != 1 {
		t.Fatalf("recovery=%+v", r)
	}
	setStatus(501)
	d.Dispatch(t.Context(), 5, "reset.announced", true)
	if n := strings.Count(logs.String(), "push not configured"); n != 2 {
		t.Fatalf("second outage logged %d times total, want 2", n)
	}
}

func TestDisabled(t *testing.T) {
	var logs bytes.Buffer
	d := NewDispatcher(&memoryStore{}, "", Disabled{}, log.New(&logs, "", 0))
	defer d.Close()
	for range 2 {
		if r := d.Dispatch(t.Context(), 1, "run.started", true); r.Configured || r.Woken != 0 {
			t.Fatal(r)
		}
	}
	if strings.Count(logs.String(), "push not configured") != 1 {
		t.Fatalf("logs=%s", &logs)
	}
}

type blockingTransport struct {
	entered chan struct{}
	release chan struct{}
}

func (b blockingTransport) Wake(ctx context.Context, _ Wake) error {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestDispatcherCoalescesQueuedDevices(t *testing.T) {
	b := blockingTransport{make(chan struct{}, 8), make(chan struct{})}
	data := &memoryStore{registrations: []store.PushRegistration{{DeviceSessionID: "phone"}}}
	d := NewDispatcher(data, "", b, log.New(&bytes.Buffer{}, "", 0))
	defer d.Close()
	for i := range 4 {
		d.Dispatch(t.Context(), int64(i+1), "run.started", false)
		select {
		case <-b.entered:
		case <-time.After(time.Second):
			t.Fatal("worker not started")
		}
	}
	d.Dispatch(t.Context(), 5, "run.started", false)
	d.Dispatch(t.Context(), 6, "run.started", false)
	if d.Stats().Coalesced != 1 {
		t.Fatalf("stats=%+v", d.Stats())
	}
	close(b.release)
}

type recordingTransport struct {
	mu     sync.Mutex
	tokens []string
	gate   chan struct{}
}

func (r *recordingTransport) Wake(_ context.Context, w Wake) error {
	<-r.gate
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, w.Token)
	return nil
}

// A device that refreshes its push token while a wake is queued must still be
// woken, at the new token, rather than losing both the queued and the
// coalesced wake to the stale-token check.
func TestQueuedWakeFollowsTokenRefresh(t *testing.T) {
	transport := &recordingTransport{gate: make(chan struct{})}
	data := &memoryStore{registrations: []store.PushRegistration{
		{DeviceSessionID: "busy-1", Token: "b1"}, {DeviceSessionID: "busy-2", Token: "b2"},
		{DeviceSessionID: "busy-3", Token: "b3"}, {DeviceSessionID: "busy-4", Token: "b4"},
		{DeviceSessionID: "phone", Token: "old"},
	}}
	d := NewDispatcher(data, "", transport, log.New(&bytes.Buffer{}, "", 0))
	defer d.Close()
	// Workers block on the four busy devices; the phone's wake waits queued.
	d.Dispatch(t.Context(), 1, "reset.announced", false)
	deadline := time.Now().Add(time.Second)
	for {
		d.mu.Lock()
		queued := len(d.queue)
		d.mu.Unlock()
		if queued == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	data.mu.Lock()
	data.registrations[4].Token = "new"
	data.mu.Unlock()
	d.Dispatch(t.Context(), 2, "reset.announced", false)
	close(transport.gate)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		got := strings.Join(transport.tokens, ",")
		transport.mu.Unlock()
		if strings.Contains(got, "new") {
			if strings.Contains(got, "old") {
				t.Fatalf("stale token woken: %s", got)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("refreshed device was never woken")
}
