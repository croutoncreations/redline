package apiclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/apiclient"
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

func TestClientTransportErrorNamesServiceAndHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := server.URL
	server.Close()
	client := apiclient.Client{BaseURL: unreachable}
	err := client.Do(context.Background(), http.MethodGet, "/v1/tasks", nil, nil)
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}
	message := err.Error()
	for _, want := range []string{"GET /v1/tasks", "cannot reach Redline at " + unreachable, "connection refused", "redline serve", "--api"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error = %q, want it to contain %q", message, want)
		}
	}
	// net/http repeats method and URL in its own wrapper; the message must not.
	if strings.Count(message, unreachable+"/v1/tasks") != 0 || strings.Contains(message, `Get "`) {
		t.Fatalf("error = %q repeats the request URL", message)
	}
}

func TestClientTransportErrorPreservesCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := apiclient.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	err := client.Do(ctx, http.MethodGet, "/v1/tasks", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
}

func TestClientUnauthorizedExplainsTokenState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"Redline API authentication is required"}`)
	}))
	defer server.Close()
	secret := "super-secret-token-value-0123456789abcdef"
	tests := []struct {
		name   string
		client apiclient.Client
		want   []string
	}{
		{"unreadable token file", apiclient.Client{TokenProblem: `API token "/etc/redline/api-token" has mode 0644`}, []string{"401", "no API token was sent", "/etc/redline/api-token"}},
		{"no token configured", apiclient.Client{}, []string{"401", "no API token was sent", "REDLINE_API_TOKEN"}},
		{"token rejected", apiclient.Client{Token: secret}, []string{"401", "service rejected the API token", "rotated"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.client.BaseURL = server.URL
			test.client.HTTPClient = server.Client()
			err := test.client.Do(context.Background(), http.MethodGet, "/v1/tasks", nil, nil)
			if err == nil {
				t.Fatal("error = nil, want non-nil")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
				}
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks the token: %q", err.Error())
			}
		})
	}
}

func TestClientNonAuthErrorsGetNoTokenHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer server.Close()
	client := apiclient.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	err := client.Do(context.Background(), http.MethodGet, "/v1/tasks", nil, nil)
	if err == nil || strings.Contains(err.Error(), "token") {
		t.Fatalf("error = %v, want no token hint on a 400", err)
	}
}
