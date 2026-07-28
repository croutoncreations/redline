package apiclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/apiclient"
)

func TestClientEncodesRequestAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s content-type=%s", r.Method, r.Header.Get("Content-Type"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer local-redline-token" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	var output map[string]string
	client := apiclient.Client{BaseURL: server.URL, Token: "local-redline-token", HTTPClient: server.Client()}
	if err := client.Do(context.Background(), http.MethodPost, "/test", map[string]bool{"run": true}, &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "ok" {
		t.Fatalf("output = %#v", output)
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer server.Close()
	client := apiclient.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	err := client.Do(context.Background(), http.MethodGet, "/v1/tasks/xyz", nil, nil)
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}
	// The message must carry method, path, status code, and server detail so
	// a CLI/MCP caller can tell which of dozens of endpoints failed and why,
	// instead of a bare "Redline API: bad request" indistinguishable from
	// any other failing request.
	for _, want := range []string{"GET", "/v1/tasks/xyz", "400", "bad request"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestClientAPIErrorFallsBackToStatusText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client := apiclient.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	err := client.Do(context.Background(), http.MethodDelete, "/v1/tasks/missing", nil, nil)
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}
	for _, want := range []string{"DELETE", "/v1/tasks/missing", "404", "Not Found"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestClientTransportErrorIncludesMethodAndPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := server.URL
	server.Close() // closed server: connections will be refused
	client := apiclient.Client{BaseURL: unreachable, HTTPClient: server.Client()}
	err := client.Do(context.Background(), http.MethodPost, "/v1/runs", nil, nil)
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}
	for _, want := range []string{"POST", "/v1/runs"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}
