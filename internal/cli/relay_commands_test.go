package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/cli"
)

func TestRelayCLIUsesAuthenticatedManagementAPIAndNeverPrintsLicense(t *testing.T) {
	const apiToken = "local-api-token-that-is-long-enough"
	const license = "rl_test_cli_never_print"
	t.Setenv("REDLINE_API_TOKEN", apiToken)
	type observed struct {
		method string
		path   string
		body   map[string]any
	}
	requests := make(chan observed, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiToken {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if r.Body != nil && r.ContentLength != 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		requests <- observed{method: r.Method, path: r.URL.EscapedPath(), body: body}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/relay/devices":
			_, _ = fmt.Fprint(w, `{"devices":[{"id":"device-1","label":"desk","first_seen":"2026-01-01T00:00:00Z","current":true}]}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = fmt.Fprint(w, `{"state":"self_hosted","mode":"self_hosted","url":"https://relay.example","connection":"connecting"}`)
		}
	}))
	defer server.Close()

	tests := []struct {
		args       []string
		method     string
		path       string
		assertBody func(*testing.T, map[string]any)
	}{
		{args: []string{"relay", "status"}, method: http.MethodGet, path: "/v1/relay/status"},
		{args: []string{"relay", "activate", license, "--label", "work mac"}, method: http.MethodPost, path: "/v1/relay/configure", assertBody: func(t *testing.T, body map[string]any) {
			if body["mode"] != "hosted" || body["license_key"] != license || body["label"] != "work mac" {
				t.Fatalf("activation body = %v", body)
			}
		}},
		{args: []string{"relay", "devices"}, method: http.MethodGet, path: "/v1/relay/devices"},
		{args: []string{"relay", "device", "deactivate", "device/opaque"}, method: http.MethodDelete, path: "/v1/relay/devices/device%2Fopaque"},
		{args: []string{"relay", "setup", "--url", "https://relay.example"}, method: http.MethodPost, path: "/v1/relay/configure", assertBody: func(t *testing.T, body map[string]any) {
			if body["mode"] != "self_hosted" || body["url"] != "https://relay.example" {
				t.Fatalf("setup body = %v", body)
			}
		}},
		{args: []string{"relay", "off"}, method: http.MethodPost, path: "/v1/relay/configure"},
		{args: []string{"relay", "deactivate"}, method: http.MethodPost, path: "/v1/relay/deactivate"},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--api", server.URL}, test.args...)
			if exit := cli.Run(args, &stdout, &stderr, time.Now); exit != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String(), license) || strings.Contains(stderr.String(), license) {
				t.Fatalf("license leaked: stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			got := <-requests
			if got.method != test.method || got.path != test.path {
				t.Fatalf("request=%s %s want %s %s", got.method, got.path, test.method, test.path)
			}
			if test.assertBody != nil {
				test.assertBody(t, got.body)
			}
		})
	}
}
