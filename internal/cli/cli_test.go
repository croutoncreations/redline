package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/cli"
)

func TestTaskDispatchConsumesServiceAPIWithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/tasks/review%2Fauth/dispatch" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("body = %q err=%v", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"run":{"id":"run-1","task_id":"review/auth","state":"preparing"}}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "task", "dispatch", "review/auth"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"task_id": "review/auth"`) || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestCandidatesConsumesProviderAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/v1/providers/team%2Fcodex/candidates" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = fmt.Fprint(w, `{"provider_account_id":"team/codex","dispatch_available":true,"candidates":[{"task_id":"review","eligible":true}]}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "candidates", "--provider", "team/codex"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"dispatch_available": true`) || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestTaskUsageIncludesDispatchAndControls(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"task"}, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "enable|disable|retry|dispatch") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestCandidatesRequiresProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"candidates"}, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "--provider is required") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestPairQRWithExplicitTrustedHost(t *testing.T) {
	configPath, token := writePairingConfig(t, []string{"redline.example.ts.net"})
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--config", configPath, "pair", "--qr", "--host", "redline.example.ts.net"}, &stdout, &stderr, time.Now)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "redline.example.ts.net") ||
		!strings.Contains(stdout.String(), "WARNING") || !strings.Contains(stdout.String(), "full API access") ||
		strings.Contains(stdout.String(), "access_token") || strings.Contains(stdout.String(), token) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestPairQRDiscoversTailscaleDNSName(t *testing.T) {
	configPath, _ := writePairingConfig(t, []string{"redline.tailnet.ts.net"})
	bin := t.TempDir()
	script := filepath.Join(bin, "tailscale")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"Self\":{\"DNSName\":\"redline.tailnet.ts.net.\"}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--config", configPath, "pair", "--qr"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), "redline.tailnet.ts.net") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestPairQRRejectsUntrustedHostAndBadUsage(t *testing.T) {
	configPath, _ := writePairingConfig(t, []string{"trusted.example.ts.net"})
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"untrusted":  {[]string{"--config", configPath, "pair", "--qr", "--host", "evil.example.ts.net"}, "api.trusted_hosts"},
		"missing qr": {[]string{"--config", configPath, "pair"}, "--qr is required"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := cli.Run(test.args, &stdout, &stderr, time.Now)
			if exit != 1 || !strings.Contains(stderr.String(), test.want) || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func writePairingConfig(t *testing.T, trustedHosts []string) (string, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "redline.yaml")
	hosts, _ := json.Marshal(trustedHosts)
	contents := fmt.Sprintf("database: redline.db\nactive_policy: standard\napi:\n  trusted_hosts: %s\nproviders:\n  codex-main:\n    provider: codex\n    usage_source: native\n    window_weekly_cost: 0.1\npolicies:\n  standard:\n    trigger_margin: 0.02\n    rolling_reserve: 0.25\n", hosts)
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("pairing-secret-", 3)
	if err := os.WriteFile(filepath.Join(root, "api-token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, token
}

func TestHelpIsSuccessfulAndIncludesProjectLinks(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--help"}, &stdout, &stderr, time.Now)
	if exit != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "usage: redline") ||
		!strings.Contains(stdout.String(), "github.com/croutoncreations/redline") ||
		!strings.Contains(stdout.String(), "utm_medium=cli") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestDemoListDocumentsAvailableScenes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"demo", "list"}, &stdout, &stderr, time.Now)
	if exit != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "overview") ||
		!strings.Contains(stdout.String(), "attention") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestDemoServeRefusesUnsafeListeners(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:7446", "127.0.0.1:7436"} {
		t.Run(listen, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := cli.Run([]string{"demo", "serve", "--listen", listen}, &stdout, &stderr, time.Now)
			if exit != 1 || !strings.Contains(stderr.String(), "demo") {
				t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDemoServeRefusesNonEmptyStateDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"demo", "serve", "--listen", "127.0.0.1:0", "--state-dir", root}, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "must be empty") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(contents) != "do not touch" {
		t.Fatalf("existing state changed: contents=%q err=%v", contents, err)
	}
}

func TestServeClaimsListenerBeforeOpeningDatabaseOrStartingScheduler(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	root := t.TempDir()
	database := filepath.Join(root, "redline.db")
	configPath := filepath.Join(root, "redline.yaml")
	config := fmt.Sprintf(`
database: %s
active_policy: standard
scheduler:
  enabled: false
usage_monitor:
  enabled: false
providers:
  codex-main:
    provider: codex
    usage_source: native
    window_weekly_cost: 0.10
policies:
  standard:
    trigger_margin: 0.02
    rolling_reserve: 0.25
`, database)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--config", configPath, "serve", "--listen", occupied.Addr().String()},
		&stdout, &stderr, time.Now,
	)
	if exit != 1 || !strings.Contains(stderr.String(), "address already in use") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatalf("database was touched before listener ownership was established: %v", err)
	}
}

func TestDecisionCommandConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/providers/codex-main/decision" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
          "snapshot":{"provider":"codex","observed_at":"2026-07-16T18:00:00Z",
            "weekly":{"remaining":0.60,"resets_at":"2026-07-18T18:00:00Z"},"source":"openusage"},
          "result":{"decision":"RUN","mode":"pace_threshold","reason":"weekly remaining meets pace threshold"}
        }`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer

	exit := cli.Run(
		[]string{"--api", server.URL, "decision", "--provider", "codex-main", "--json"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"decision": "RUN"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestSchedulerEvaluateConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/scheduler/evaluate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{
          "snapshot":{"provider":"codex","observed_at":"2026-07-16T18:00:00Z",
            "weekly":{"remaining":0.60,"resets_at":"2026-07-18T18:00:00Z"},"source":"openusage"},
          "result":{"decision":"RUN","mode":"pace_threshold","reason":"weekly remaining meets pace threshold"},
          "selected_task":{"id":"review","name":"Review auth","state":"queued"}
        }`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "scheduler", "evaluate", "--provider", "codex-main", "--json"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"id": "review"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestSchedulerEvaluateExplainsRejectedTasks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
          "snapshot":{"provider":"codex","observed_at":"2026-07-16T18:00:00Z",
            "weekly":{"remaining":0.60,"resets_at":"2026-07-18T18:00:00Z"},"source":"openusage"},
          "result":{"decision":"RUN","mode":"pace_threshold","reason":"weekly remaining meets pace threshold",
            "task_selection_reason":"no queued tasks are eligible",
            "candidate_rejections":[{"task_id":"tests","reason":"cooldown until 2026-07-17T18:00:00Z"}]}
        }`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer

	exit := cli.Run(
		[]string{"--api", server.URL, "scheduler", "evaluate", "--provider", "codex-main"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), "Task selection:") ||
		!strings.Contains(stdout.String(), "no queued tasks are eligible") ||
		!strings.Contains(stdout.String(), "Rejected tests:") ||
		!strings.Contains(stdout.String(), "cooldown until") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestTaskAddImportsYAMLThroughAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"tests","name":"Add tests","type":"recurring","state":"queued"}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(path, []byte(`
id: tests
name: Add tests
type: recurring
priority: 50
execution_profile_id: codex-devx
min_interval: 7d
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "task", "add", "--file", path, "--json"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"state": "queued"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestAPIErrorIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":"provider not found"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "status", "--provider", "missing"},
		&stdout, &stderr, time.Now,
	)
	if exit != 1 || !strings.Contains(stderr.String(), "provider not found") {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}

func TestStatusShowsSupplementalModelAllowance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{
          "provider":"claude","observed_at":"2026-07-19T03:27:53Z",
          "short":{"remaining":1,"resets_at":"2026-07-19T07:00:00Z"},
          "weekly":{"remaining":0.73,"resets_at":"2026-07-24T17:00:00Z"},
          "allowances":[
            {"key":"session","source_label":"Session","scope":"account","role":"short","remaining":1,"resets_at":"2026-07-19T07:00:00Z","period_duration_seconds":18000},
            {"key":"weekly","source_label":"Weekly","scope":"account","role":"weekly","remaining":0.73,"resets_at":"2026-07-24T17:00:00Z","period_duration_seconds":604800},
            {"key":"model:fable:weekly","source_label":"Fable","scope":"model","role":"weekly","remaining":0.48,"resets_at":"2026-07-24T17:00:00Z","period_duration_seconds":604800}
          ],"source":"openusage"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "status", "--provider", "claude-main"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), "Fable: 48.0% remaining") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestPauseCommandConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/providers/codex-main/pause" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"provider_account_id":"codex-main","paused":true}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "pause", "--provider", "codex-main"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), "paused codex-main") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestTaskDisableConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks/review/disable" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"id":"review","name":"Review","state":"disabled"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "task", "disable", "review", "--json"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"state": "disabled"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestSchedulerExecuteConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/scheduler/execute" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{
          "snapshot":{"provider":"codex","weekly":{"remaining":0.6,"resets_at":"2026-07-18T18:00:00Z"}},
          "result":{"decision":"RUN","mode":"pace_threshold"},
          "run":{"id":"run-1","task_id":"task","provider_account_id":"codex-main","state":"preparing","started_at":"2026-07-16T18:00:00Z"}
        }`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "scheduler", "execute", "--provider", "codex-main", "--json"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"state": "preparing"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestSchedulerStatusConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/scheduler/status" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"enabled":true,"poll_interval":"1m0s","running":false}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "scheduler", "status"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"enabled": true`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestHealthConsumesDetailedHealthAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/health/details" || r.URL.Query().Get("window") != "12h" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"status":"degraded","window":"12h0m0s","since":"2026-07-16T06:00:00Z","dispatch_errors":1}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "health", "--window", "12h"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"status": "degraded"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestNotificationListConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/notifications" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `[{"id":1,"event_type":"run.failed","status":"failed","payload":{},"attempts":1,"created_at":"2026-07-16T18:00:00Z","updated_at":"2026-07-16T18:00:01Z"}]`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "notification", "list"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"status": "failed"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestSchedulerAttemptsConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/scheduler/attempts" || r.URL.Query().Get("provider") != "codex-main" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `[{"id":1,"provider_account_id":"codex-main","trigger":"automatic","outcome":"error","error":"offline","started_at":"2026-07-16T18:00:00Z","completed_at":"2026-07-16T18:00:01Z"}]`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "scheduler", "attempts", "--provider", "codex-main"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"outcome": "error"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestRunShowConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/run-1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"id":"run-1","task_id":"task","state":"completed","started_at":"2026-07-16T18:00:00Z"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "run", "show", "run-1"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"state": "completed"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestRunLogsConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1/logs" ||
			r.URL.Query().Get("stream") != "stderr" || r.URL.Query().Get("tail_bytes") != "2048" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"content":"agent failed\n","size_bytes":13,"truncated":false}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "run", "logs", "run-1", "--stream", "stderr", "--tail-bytes", "2048"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || stdout.String() != "agent failed\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestRunEventsConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/run-1/events" || r.URL.Query().Get("limit") != "25" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `[{"id":1,"run_id":"run-1","type":"run.started","occurred_at":"2026-07-16T18:00:00Z","payload":{}}]`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "run", "events", "run-1", "--limit", "25"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"type": "run.started"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestCalibrationConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/providers/claude-main/calibration" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = fmt.Fprint(w, `{"provider":"claude","configured_cost":0.08,"observed_cost":0.082,"effective_cost":0.082,"source":"observed","confidence":"medium","informative_windows":2,"total_short_usage":1.1}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run(
		[]string{"--api", server.URL, "calibration", "--provider", "claude-main"},
		&stdout, &stderr, time.Now,
	)
	if exit != 0 || !strings.Contains(stdout.String(), `"source": "observed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestCapacityConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/providers/claude-main/capacity" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"provider":"claude","confidence":"low","snapshot_count":2,"token_observation_count":3,"calculated_at":"2026-07-17T00:00:00Z"}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "capacity", "--provider", "claude-main"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"provider": "claude"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestLaunchMetricsConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/metrics/launch" ||
			r.URL.Query().Get("days") != "14" || r.URL.Query().Get("provider") != "claude-main" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"since":"2026-08-12T00:00:00Z","until":"2026-08-26T00:00:00Z","decisions":{"automatic_checks":10,"run":2,"wait":8,"unknown":0,"errors":0,"no_eligible_task":1,"wait_rate":0.8},"jobs":{},"providers":[],"methodology":[]}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "metrics", "launch", "--days", "14", "--provider", "claude-main"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"wait_rate": 0.8`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}

func TestTokenSyncConsumesServiceAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/providers/codex-main/token-sync" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"provider":"codex","read":4,"inserted":3}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", server.URL, "token", "sync", "--provider", "codex-main"}, &stdout, &stderr, time.Now)
	if exit != 0 || !strings.Contains(stdout.String(), `"inserted": 3`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
}
