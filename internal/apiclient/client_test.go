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
	err := client.Do(context.Background(), http.MethodGet, "/test", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("error = %v", err)
	}
}
