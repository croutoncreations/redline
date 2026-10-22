package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/apiclient"
)

func TestDashboardOpensAuthenticatedLocalSessionWithoutPrintingToken(t *testing.T) {
	const token = "test-dashboard-token-kept-out-of-output"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected health probe: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{ "status": "ok" }`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	var opened string
	code := runDashboard(apiclient.Client{BaseURL: server.URL, Token: token}, nil, &stdout, &stderr, func(value string) error { opened = value; return nil })
	address, err := url.Parse(opened)
	if err != nil || code != 0 || address.Query().Get("access_token") != token || address.Path != "/" {
		t.Fatalf("code=%d opened valid=%v error=%v stderr=%s", code, opened != "", err, &stderr)
	}
	if strings.Contains(stdout.String()+stderr.String(), token) || !strings.Contains(stdout.String(), server.URL) {
		t.Fatal("dashboard output must include only the token-free origin")
	}
}

func TestDashboardRefusesUnsafeDestinationsBeforeOpeningBrowser(t *testing.T) {
	for _, address := range []string{"https://127.0.0.1:7436", "http://example.com", "http://user@localhost:7436", "http://localhost:7436/proxy", "http://localhost:7436/?x=y", "http://localhost:7436/#fragment", "http://localhost:7436\\@example.com"} {
		t.Run(address, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			opened := false
			code := runDashboard(apiclient.Client{BaseURL: address, Token: "secret"}, nil, &stdout, &stderr, func(string) error { opened = true; return nil })
			if code == 0 || opened || strings.Contains(stderr.String(), "secret") {
				t.Fatal("unsafe destination was opened or credential leaked")
			}
		})
	}
}

func TestDashboardMissingTokenAndBrowserErrorsAreActionableAndRedacted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if runDashboard(apiclient.Client{BaseURL: "http://127.0.0.1:7436"}, nil, &stdout, &stderr, nil) == 0 || !strings.Contains(stderr.String(), "--config") {
		t.Fatal("missing token should explain how to use the matching config")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer server.Close()
	stderr.Reset()
	if runDashboard(apiclient.Client{BaseURL: server.URL, Token: "secret"}, nil, &stdout, &stderr, func(address string) error { return errors.New(address) }) == 0 || strings.Contains(stderr.String(), "secret") {
		t.Fatal("browser error must not leak credential-bearing URL")
	}
}

func TestDashboardDoesNotOpenAfterServiceAuthenticationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret", http.StatusUnauthorized) }))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	opened := false
	code := runDashboard(apiclient.Client{BaseURL: server.URL, Token: "secret"}, nil, &stdout, &stderr, func(string) error { opened = true; return nil })
	if code == 0 || opened || strings.Contains(stderr.String(), "secret") || !strings.Contains(stderr.String(), "--config") {
		t.Fatal("failed authentication must not open the browser or disclose credentials")
	}
}
