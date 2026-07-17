package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/scheduler"
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

func TestSchedulerStatusIsExposedWhenDisabled(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/scheduler/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var status scheduler.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.PollInterval != "5m0s" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAutomaticSchedulerResolvesEachTaskRepositoryAndRecordsTrigger(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	for _, profile := range []domain.ExecutionProfile{
		{ID: "unchanged", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo/a"},
		{ID: "changed", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo/b"},
	} {
		if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "skip", Name: "Skip", Priority: 100, ExecutionProfileID: "unchanged", Type: domain.Recurring, RequireRepoChange: true, LastSuccessfulSourceRevision: "same"},
		{ID: "run", Name: "Run", Priority: 80, ExecutionProfileID: "changed", Type: domain.OneOff, RequireRepoChange: true, LastSuccessfulSourceRevision: "old"},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	executed := make(chan domain.Task, 1)
	handler := api.NewServerWithDependencies(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
			executed <- task
			return nil
		},
	}, fakeRevisionResolver{revisions: map[string]string{"unchanged": "same", "changed": "new"}})
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	select {
	case task := <-executed:
		if task.ID != "run" {
			t.Fatalf("executed task = %#v", task)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic scheduler did not dispatch")
	}
	cancel()
	handler.Wait()
	decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
	if err != nil || len(decisions) != 1 || !bytes.Contains(decisions[0].DecisionJSON, []byte(`"trigger":"automatic"`)) {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
}

func TestAutomaticSchedulerSkipsActiveProviderWithoutFetchingUsage(t *testing.T) {
	var requests atomic.Int32
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	delete(cfg.Providers, "claude-main")
	cfg.Scheduler = config.Scheduler{Enabled: true, PollInterval: "1h"}
	profile := domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "Task", ExecutionProfileID: "profile", Type: domain.OneOff}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "existing-run", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		decisions, err := db.ListSchedulerDecisions(t.Context(), "codex-main", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) == 1 {
			found = true
			if !bytes.Contains(decisions[0].DecisionJSON, []byte(`"mode":"active_run"`)) {
				t.Fatalf("decision = %s", decisions[0].DecisionJSON)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	handler.Wait()
	if !found {
		t.Fatal("automatic active-run decision was not recorded")
	}
	if requests.Load() != 0 {
		t.Fatalf("OpenUsage requests = %d", requests.Load())
	}
}

func TestEmptyRunListIsJSONArray(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if strings.TrimSpace(body.String()) != "[]" {
		t.Fatalf("body = %s", body.String())
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

func TestExecuteAdmitsTaskAndStartsExecutor(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	executed := make(chan domain.Run, 1)
	handler := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, run domain.Run, _ domain.Task, _ domain.ExecutionProfile) error {
			executed <- run
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "review", "name": "Review auth", "prompt": "review", "priority": 80,
		"execution_profile_id": "codex-devx", "type": "one_off",
	})
	response := postJSON[struct {
		Run *domain.Run `json:"run"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if response.Run == nil || response.Run.State != domain.RunPreparing {
		t.Fatalf("run = %#v", response.Run)
	}
	select {
	case got := <-executed:
		if got.ID != response.Run.ID {
			t.Fatalf("executed run %q, want %q", got.ID, response.Run.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not started")
	}
}

func TestExecuteEndToEndWithRealCommandHarness(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = filepath.Join(t.TempDir(), "runs")
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	defer handler.Wait()
	server := httptest.NewServer(handler)
	defer server.Close()
	repository := t.TempDir()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "command-local", "provider_account_id": "codex-main",
		"harness_type": "command", "harness_command": "printf '{\"done\":true}\\n'",
		"workspace_provider": "existing-directory", "repository": repository,
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "command-task", "name": "Command task", "prompt": "safe prompt", "priority": 80,
		"execution_profile_id": "command-local", "type": "one_off",
	})
	response := postJSON[struct {
		Run *domain.Run `json:"run"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	if response.Run == nil {
		t.Fatal("expected admitted run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := db.GetRun(t.Context(), response.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == domain.RunCompleted {
			if run.OutputFile == "" {
				t.Fatal("completed run has no output file")
			}
			data, err := os.ReadFile(run.OutputFile)
			if err != nil || !bytes.Contains(data, []byte(`"done":true`)) {
				t.Fatalf("output=%s err=%v", data, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not complete")
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
	cfg := testConfig(usage.URL)
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, db
}

func testConfig(usageURL string) config.Config {
	return config.Config{
		Database: "unused", ActivePolicy: "standard", MaxSnapshotAge: "15m",
		Providers: map[string]config.Provider{
			"codex-main":  {Provider: "codex", OpenUsageURL: usageURL, WindowWeeklyCost: 0.10},
			"claude-main": {Provider: "claude", OpenUsageURL: usageURL, WindowWeeklyCost: 0.08},
		},
		Policies: map[string]config.Policy{
			"standard": {
				TriggerMargin: 0.02, RollingReserve: 0.25,
				PaceThresholds: []config.PaceThreshold{{TimeRemaining: "72h", MinWeeklyRemaining: 0.50}},
			},
		},
	}
}

type fakeExecutor struct {
	execute func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error
}

type fakeRevisionResolver struct{ revisions map[string]string }

func (f fakeRevisionResolver) Resolve(_ context.Context, profile domain.ExecutionProfile) (string, error) {
	return f.revisions[profile.ID], nil
}

func (f fakeExecutor) Execute(ctx context.Context, run domain.Run, task domain.Task, profile domain.ExecutionProfile) error {
	return f.execute(ctx, run, task, profile)
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
