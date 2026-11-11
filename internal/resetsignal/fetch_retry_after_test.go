package resetsignal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetcherPreservesRetryAfterOnFinalAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(429)
	}))
	defer server.Close()
	f := Fetcher{HTTP: server.Client(), AllowHTTP: true, Sleep: func(context.Context, time.Duration) error { return nil }}
	_, err := f.get(t.Context(), "resetbeacon", server.URL)
	if err == nil || err.Metadata.RetryAfter != 2*time.Hour || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%+v", calls.Load(), err)
	}
}
