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

	"github.com/croutoncreations/redline/internal/cli"
)

const primerStatusJSON = `{"settings":{"provider_account_id":"claude-main","enabled":true,"mode":"schedule",
"times":["06:00","11:00"],"days":["mon","tue","wed","thu","fri"],"timezone":"America/Chicago",
"prompt":"Reply with only: ok","model":"haiku","catch_up_seconds":2700},"configured":true,"supported":true,
"timezone":"America/Chicago","next_ping_at":"2026-09-29T11:01:00Z",
"forecast":[{"start":"2026-09-29T11:00:00Z","end":"2026-09-29T16:00:00Z"}]}`

func TestPrimerSetSendsOnlyTheFlagsGiven(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/providers/claude-main/primer" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = fmt.Fprint(w, primerStatusJSON)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "primer", "set", "--provider", "claude-main",
		"--mode", "schedule", "--at", "06:00, 11:00", "--days", "weekdays", "--tz", "America/Chicago",
		"--catch-up", "30m", "--enable"}, &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if body["mode"] != "schedule" || body["enabled"] != true || body["timezone"] != "America/Chicago" ||
		body["catch_up_seconds"] != float64(1800) || fmt.Sprint(body["times"]) != "[06:00 11:00]" {
		t.Fatalf("body=%#v", body)
	}
	if _, ok := body["prompt"]; ok {
		t.Fatalf("unset flags must not be sent: %#v", body)
	}
	for _, want := range []string{"Window primer: on", "06:00, 11:00", "Next ping:", "Forecast:      06:00–11:00"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestPrimerRunForwardsForceAndFailsOnFailedPing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/providers/claude-main/primer/run" || r.URL.Query().Get("force") != "true" {
			t.Errorf("request = %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"outcome":"failed","reason":"claude exited with code 1"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "primer", "run", "--provider", "claude-main", "--force"},
		&stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stdout.String(), "failed: claude exited") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestPrimerSetRejectsBadInputBeforeCallingTheService(t *testing.T) {
	for _, args := range [][]string{
		{"primer", "set", "--provider", "claude-main"},
		{"primer", "set", "--provider", "claude-main", "--mode", "hourly"},
		{"primer", "set", "--provider", "claude-main", "--catch-up", "soon"},
		{"primer", "set", "--provider", "claude-main", "--enable", "--disable"},
		{"primer", "status"},
		{"primer", "frobnicate", "--provider", "claude-main"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := cli.Run(append([]string{"--api", "http://127.0.0.1:1"}, args...), &stdout, &stderr, time.Now); exit == 0 {
			t.Errorf("%v: expected failure", args)
		}
	}
}
