package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

var apiNow = time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)

func TestServiceTaskAndSimulatedSchedulerFlow(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	profile := postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	if profile.ID != "codex-devx" {
		t.Fatalf("profile = %#v", profile)
	}
	task := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "review", "name": "Review auth", "priority": 80,
		"execution_profile_id": "codex-devx", "type": "one_off",
	})
	if task.State != domain.Queued {
		t.Fatalf("task = %#v", task)
	}

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{
		"provider_account_id": "codex-main",
	})
	if result.Result.Decision != decision.Run || result.Result.Mode != decision.ModePace {
		t.Fatalf("decision = %#v", result.Result)
	}
	if result.SelectedTask == nil || result.SelectedTask.ID != "review" {
		t.Fatalf("selected task = %#v", result.SelectedTask)
	}
	decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	stored, err := db.GetTask(t.Context(), "review")
	if err != nil || stored.State != domain.Queued {
		t.Fatalf("simulation mutated task: %#v err=%v", stored, err)
	}
	resp, err := http.Get(server.URL + "/v1/scheduler/decisions?provider=codex-main")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var history []domain.SchedulerDecision
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !bytes.Contains(history[0].DecisionJSON, []byte(`"decision":"RUN"`)) {
		t.Fatalf("history = %#v", history)
	}
}

func TestServiceProviderStatusAndDecision(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	refresh := postJSON[decision.UsageSnapshot](
		t, server.URL+"/v1/providers/claude-main/refresh", map[string]any{},
	)
	if refresh.Short == nil || refresh.Weekly.Remaining < 0.679999 || refresh.Weekly.Remaining > 0.680001 {
		t.Fatalf("refresh = %#v", refresh)
	}

	resp, err := http.Get(server.URL + "/v1/providers/claude-main/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", resp.StatusCode)
	}
	var status decision.UsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Provider != "claude" {
		t.Fatalf("status = %#v", status)
	}
}

func TestServiceHealth(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestPausedProviderDoesNotSelectTask(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/providers/codex-main/pause", map[string]any{})
	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{
		"provider_account_id": "codex-main",
	})
	if result.Result.Decision != decision.Wait || result.Result.Reason != "provider is paused" {
		t.Fatalf("result = %#v", result.Result)
	}
}

func newAPIServer(t *testing.T, payload string) (*httptest.Server, *store.DB) {
	t.Helper()
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, payload)
	}))
	t.Cleanup(usage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := config.Config{
		Database: "unused", ActivePolicy: "standard", MaxSnapshotAge: "15m",
		Providers: map[string]config.Provider{
			"codex-main":  {Provider: "codex", OpenUsageURL: usage.URL, WindowWeeklyCost: 0.10},
			"claude-main": {Provider: "claude", OpenUsageURL: usage.URL, WindowWeeklyCost: 0.08},
		},
		Policies: map[string]config.Policy{
			"standard": {
				TriggerMargin: 0.02, RollingReserve: 0.25,
				PaceThresholds: []config.PaceThreshold{{TimeRemaining: "72h", MinWeeklyRemaining: 0.50}},
			},
		},
	}
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, db
}

func postJSON[T any](t *testing.T, url string, body any) T {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem any
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		t.Fatalf("POST %s: status=%d body=%#v", url, resp.StatusCode, problem)
	}
	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

const codexPayload = `{
  "providerId":"codex", "fetchedAt":"2026-07-16T18:00:00Z",
  "lines":[{"type":"progress","label":"Weekly","used":40,"limit":100,
  "resetsAt":"2026-07-18T18:00:00Z"}]}`

const claudePayload = `{
  "providerId":"claude", "fetchedAt":"2026-07-16T18:00:00Z",
  "lines":[
    {"type":"progress","label":"Session","used":20,"limit":100,"resetsAt":"2026-07-16T20:00:00Z"},
    {"type":"progress","label":"Weekly","used":32,"limit":100,"resetsAt":"2026-07-17T17:00:00Z"}
  ]}`
