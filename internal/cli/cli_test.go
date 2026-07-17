package cli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/cli"
)

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
