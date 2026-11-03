package push

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/store"
)

func TestRelayUnregisteredErrorCode(t *testing.T) {
	for _, body := range []string{`{"code":"unregistered"}`, `{"error":"unregistered"}`, `{"error":{"code":"unregistered"}}`} {
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); _, _ = w.Write([]byte(body)) }))
		transport := &RelayTransport{BaseURL: relay.URL}
		if err := transport.Wake(t.Context(), Wake{}); !errors.Is(err, ErrUnregistered) {
			t.Fatalf("err=%v", err)
		}
		relay.Close()
	}
}

func TestDispatcherOverflowAndShutdown(t *testing.T) {
	b := blockingTransport{make(chan struct{}, 4), make(chan struct{})}
	data := &memoryStore{}
	for i := range 260 {
		data.registrations = append(data.registrations, store.PushRegistration{DeviceSessionID: fmt.Sprint(i)})
	}
	d := NewDispatcher(data, "", b, log.New(&bytes.Buffer{}, "", 0))
	// Dispatch holds mu while enqueuing. Workers can remove up to four items,
	// so fill a second batch after all workers have entered their blocked send.
	d.Dispatch(t.Context(), 1, "run.started", false)
	for range 4 {
		select {
		case <-b.entered:
		case <-time.After(time.Second):
			t.Fatal("missing active worker")
		}
	}
	data.mu.Lock()
	data.registrations = append(data.registrations, store.PushRegistration{DeviceSessionID: "extra"})
	data.mu.Unlock()
	d.Dispatch(t.Context(), 2, "run.started", false)
	if d.Stats().Dropped == 0 {
		t.Fatal("overflow was not counted")
	}
	stopped := make(chan struct{})
	go func() { d.Close(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel sends")
	}
	d.Close() // idempotent
}

func TestWakeHintKind(t *testing.T) {
	for event, want := range map[string]string{"reset.expiring": "reset", "firesale.started": "firesale", "run.started": "job", "scheduler.error": "job", "test": "test", "arbitrary secret text": "job"} {
		if got := kind(event); got != want {
			t.Errorf("kind(%q)=%q want %q", event, got, want)
		}
	}
}
