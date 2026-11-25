package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPushHungDirectFallsBackWithinThreeSeconds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(server.URL, "phone")
	relayed := false
	client.SetRelayFallback(RelayFallbackWithStatus(func(method, path, body string) (int, string, error) {
		relayed = true
		return 200, `[]`, nil
	}))
	start := time.Now()
	raw, err := client.FetchNotificationsAfter(1, 50)
	if err != nil || raw != "[]" || !relayed {
		t.Fatalf("fallback: %s %v relayed=%t", raw, err, relayed)
	}
	if elapsed := time.Since(start); elapsed < 2800*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("direct cap took %v, want about three seconds", elapsed)
	}
	if client.httpClient.Timeout != defaultTimeout {
		t.Fatal("push changed the app-wide timeout")
	}
}

func TestPushRelayUsesRemainingOverallDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewClient(server.URL, "phone")
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("abandoned relay callback did not finish")
		}
	}()
	client.SetRelayFallback(RelayFallbackWithStatus(func(method, path, body string) (int, string, error) {
		close(started)
		<-release
		close(finished)
		return 200, `[]`, nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, directAttemptTimeoutKey{}, 30*time.Millisecond)
	var deliveries []pushDelivery
	start := time.Now()
	err := client.get(ctx, "/v1/notifications?after_id=0&limit=50", &deliveries)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want overall deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("relay ignored remaining budget: %v", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("direct timeout did not fall back")
	}
	// The late result cannot return to the caller or fill its output.
	if deliveries != nil {
		t.Fatal("late relay decoded a result")
	}
}
