package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func shortBackoff(t *testing.T) {
	t.Helper()
	old := retryBackoff
	retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoff = old })
}

// statusServer answers with each status in turn, then repeats the last one.
func statusServer(t *testing.T, statuses ...int) (*GitHubClient, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := statuses[len(statuses)-1]
		if calls < len(statuses) {
			status = statuses[calls]
		}
		calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return &GitHubClient{apiBase: server.URL, client: server.Client()}, &calls
}

func TestRequestRetriesTransientServerErrors(t *testing.T) {
	shortBackoff(t)
	c, calls := statusServer(t, 502, 503, 200)
	data, err := c.doRequest(context.Background(), "GET", c.apiBase+"/x")
	if err != nil || string(data) != `{"ok":true}` || *calls != 3 {
		t.Fatalf("data=%q err=%v calls=%d", data, err, *calls)
	}
}

func TestRequestGivesUpAfterBoundedRetries(t *testing.T) {
	shortBackoff(t)
	c, calls := statusServer(t, 500)
	_, err := c.doRequest(context.Background(), "GET", c.apiBase+"/x")
	if err == nil || !strings.Contains(err.Error(), "500") || *calls != len(retryBackoff)+1 {
		t.Fatalf("err=%v calls=%d", err, *calls)
	}
}

func TestRequestDoesNotRetryClientErrors(t *testing.T) {
	shortBackoff(t)
	for _, status := range []int{401, 403, 404, 422} {
		c, calls := statusServer(t, status, 200)
		if _, err := c.doRequest(context.Background(), "GET", c.apiBase+"/x"); err == nil || *calls != 1 {
			t.Fatalf("status %d: err=%v calls=%d", status, err, *calls)
		}
	}
}

func TestRequestRetriesTransportFailures(t *testing.T) {
	shortBackoff(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			// Drop the connection mid-response, like a reset or stream cancel.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	c := &GitHubClient{apiBase: server.URL, client: server.Client()}
	if data, err := c.doRequest(context.Background(), "GET", server.URL+"/x"); err != nil || string(data) != "[]" || calls != 2 {
		t.Fatalf("data=%q err=%v calls=%d", data, err, calls)
	}
}

func TestRequestStopsRetryingWhenContextEnds(t *testing.T) {
	old := retryBackoff
	retryBackoff = []time.Duration{time.Hour}
	t.Cleanup(func() { retryBackoff = old })
	c, calls := statusServer(t, 503)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.doRequest(ctx, "GET", c.apiBase+"/x"); err == nil || *calls != 1 {
		t.Fatalf("err=%v calls=%d", err, *calls)
	}
}
