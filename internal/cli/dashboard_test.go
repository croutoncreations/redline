package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestDashboardCommandUsesLoopbackForWildcardListeners(t *testing.T) {
	for _, tt := range []struct{ listen, want string }{
		{"0.0.0.0:7436", "http://127.0.0.1:7436"},
		{"[::]:17436", "http://[::1]:17436"},
		{":7436", "http://127.0.0.1:7436"},
		{"127.0.0.1:17436", "http://127.0.0.1:17436"},
		{"[::1]:17436", "http://[::1]:17436"},
	} {
		if got := dashboardOrigin(tt.listen); got != tt.want {
			t.Errorf("dashboardOrigin(%q)=%q, want %q", tt.listen, got, tt.want)
		}
	}
}

func TestShellQuoteRoundTripsUntrustedConfigPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell behavior is not applicable on Windows")
	}
	for _, test := range []struct {
		name  string
		value func(string) string
	}{
		{
			name: "shell metacharacters",
			value: func(marker string) string {
				return "config path; touch " + marker + "; printf injected $(uname) $HOME"
			},
		},
		{
			name: "embedded single quote",
			value: func(marker string) string {
				return "config'; touch " + marker + "; #"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "injected")
			value := test.value(marker)
			output, err := exec.Command("/bin/sh", "-c", "printf '%s' "+shellQuote(value)).CombinedOutput()
			if err != nil {
				t.Fatalf("execute quoted value: %v: %s", err, output)
			}
			if string(output) != value {
				t.Fatalf("shell round trip = %q, want %q", output, value)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("untrusted value executed shell commands: %v", err)
			}
		})
	}
}
