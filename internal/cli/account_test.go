package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

const accountsReply = `{"restart_required":true,"accounts":[
 {"id":"claude-client","provider":"claude","label":"Client","usage_source":"openusage","openusage_card":"claude@ab12cd34","origin":"managed","pending":"add","sign_in":"Reads OpenUsage card claude@ab12cd34. Sign in in OpenUsage."},
 {"id":"claude-main","provider":"claude","usage_source":"auto","origin":"config","running":true,"status":"signed_out","last_error":"Claude Code is signed out","sign_in":"Run claude auth login."}]}`

func TestAccountAddSendsOnlyNonSecretFieldsAndSaysRestart(t *testing.T) {
	var method, path string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(accountsReply))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "account", "add", "claude-client", "--provider", "claude",
		"--label", "Client", "--card", "claude@ab12cd34"}, &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, stderr.String())
	}
	if method != http.MethodPost || path != "/v1/accounts" {
		t.Fatalf("%s %s", method, path)
	}
	want := map[string]any{"id": "claude-client", "provider": "claude", "label": "Client",
		"openusage_card": "claude@ab12cd34", "usage_source": "openusage"}
	if len(body) != len(want) {
		t.Fatalf("body = %v", body)
	}
	for key, value := range want {
		if body[key] != value {
			t.Fatalf("body[%s] = %v, want %v", key, body[key], value)
		}
	}
	out := stdout.String()
	if !strings.Contains(out, "added account claude-client") || !strings.Contains(out, "Sign in in OpenUsage") ||
		!strings.Contains(out, "Restart the Redline service") {
		t.Fatalf("stdout = %s", out)
	}
}

func TestAccountListShowsOriginsPendingAndSignInSteps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(accountsReply))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if exit := cli.Run([]string{"--api", server.URL, "account", "list"}, &stdout, &stderr, time.Now); exit != 0 {
		t.Fatalf("exit %d: %s", exit, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"pending add (restart)", "default login (auto)", "config", "signed_out", "Run claude auth login."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAccountEditCannotChangeProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := cli.Run([]string{"--api", "http://127.0.0.1:1", "account", "edit", "x", "--provider", "codex"}, &stdout, &stderr, time.Now); exit == 0 {
		t.Fatal("changing provider must be refused")
	}
}
