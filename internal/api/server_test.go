package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jfox/redline/internal/api"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/discovery"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/workspace"
)

var apiNow = time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)

func TestDashboardPageAndAssetsAreServed(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "gas-gauge"},
		{path: "/", contentType: "text/html", contains: "+ New job"},
		{path: "/", contentType: "text/html", contains: "Run when"},
		{path: "/", contentType: "text/html", contains: "EXECUTION PROFILES"},
		{path: "/", contentType: "text/html", contains: "Custom command"},
		{path: "/", contentType: "text/html", contains: "Recently used repositories"},
		{path: "/", contentType: "text/html", contains: "Allowance routing override"},
		{path: "/assets/dashboard.css", contentType: "text/css", contains: ":root"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "Recent errors"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "method:id ? 'PATCH' : 'POST'"},
		{path: "/assets/dashboard.js", contentType: "text/javascript", contains: "/v1/profile-options"},
		{path: "/assets/claude.svg", contentType: "image/svg+xml", contains: "<title>Claude</title>"},
		{path: "/assets/codex.svg", contentType: "image/svg+xml", contains: "<title>Codex</title>"},
	} {
		resp, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), test.contentType) || !strings.Contains(body.String(), test.contains) {
			t.Fatalf("GET %s: status=%d content-type=%q body=%q", test.path, resp.StatusCode, resp.Header.Get("Content-Type"), body.String())
		}
	}
	resp, err := http.Get(server.URL + "/assets/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status = %d", resp.StatusCode)
	}
}

func TestDashboardEventsStreamAnImmediateSnapshot(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/dashboard/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buffer := make([]byte, 8192)
	n, err := resp.Body.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	body := string(buffer[:n])
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "event: dashboard\n") || !strings.Contains(body, `"active_policy":"standard"`) {
		t.Fatalf("unexpected event: %q", body)
	}
}

func TestProfileOptionsExposeDiscoveredHarnessesAndCacheUntilRefresh(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	discoverer := &fakeHarnessDiscoverer{catalog: discovery.Catalog{GeneratedAt: apiNow, Harnesses: []discovery.Harness{{
		ID: "pi", Label: "Pi", Installed: true, Version: "0.80.10",
		Models: map[string][]discovery.Model{"codex": {{ID: "openai-codex/gpt-5.6-sol", Source: "pi_catalog"}}},
	}}}}
	server := httptest.NewServer(api.NewServerWithHarnessDiscoverer(testConfig("http://unused"), db, func() time.Time { return apiNow }, discoverer))
	defer server.Close()
	for _, path := range []string{"/v1/profile-options", "/v1/profile-options", "/v1/profile-options?refresh=true"} {
		resp, getErr := http.Get(server.URL + path)
		if getErr != nil {
			t.Fatal(getErr)
		}
		var catalog discovery.Catalog
		if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || catalog.Harnesses[0].ID != "pi" {
			t.Fatalf("status=%d catalog=%#v", resp.StatusCode, catalog)
		}
	}
	if discoverer.calls.Load() != 2 {
		t.Fatalf("discovery calls = %d", discoverer.calls.Load())
	}
}

func TestDashboardReadModelIsUsefulAndDoesNotExposePrompts(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.SaveSnapshot(t.Context(), decision.UsageSnapshot{
		Provider: "codex", ObservedAt: apiNow, Weekly: decision.UsageWindow{Remaining: .86, ResetsAt: apiNow.Add(4 * 24 * time.Hour)}, Source: "test",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "codex-devx", ProviderAccountID: "codex-main", HarnessType: "codex-cli", Model: "gpt-5",
		WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "quiet-check", Name: "Quiet check", Prompt: "TOP SECRET PROMPT", Priority: 90,
		ExecutionProfileID: "codex-devx", Type: domain.Recurring, MinInterval: 24 * time.Hour,
	}, apiNow); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body.String())
	}
	if strings.Contains(body.String(), "TOP SECRET PROMPT") {
		t.Fatalf("dashboard exposed task prompt: %s", body.String())
	}
	var got struct {
		ActivePolicy string `json:"active_policy"`
		Providers    []struct {
			ID       string                  `json:"id"`
			Snapshot *decision.UsageSnapshot `json:"snapshot"`
			Error    string                  `json:"error"`
		} `json:"providers"`
		Tasks []struct {
			ID       string        `json:"id"`
			Provider string        `json:"provider_account_id"`
			Model    string        `json:"model"`
			Interval time.Duration `json:"min_interval"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ActivePolicy != "standard" || len(got.Providers) != 2 || len(got.Tasks) != 1 {
		t.Fatalf("dashboard = %#v", got)
	}
	if got.Providers[0].ID != "claude-main" || got.Providers[0].Snapshot != nil || got.Providers[0].Error == "" {
		t.Fatalf("missing-provider state = %#v", got.Providers[0])
	}
	if got.Providers[1].ID != "codex-main" || got.Providers[1].Snapshot == nil || got.Providers[1].Snapshot.Weekly.Remaining != .86 {
		t.Fatalf("codex provider = %#v", got.Providers[1])
	}
	if got.Tasks[0].ID != "quiet-check" || got.Tasks[0].Provider != "codex-main" || got.Tasks[0].Model != "gpt-5" || got.Tasks[0].Interval != 24*time.Hour {
		t.Fatalf("task projection = %#v", got.Tasks[0])
	}
}

func TestDashboardReportsStoreFailures(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestRunDetailEndpointReturnsRunAndNotFound(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "run-profile", "provider_account_id": "codex-main", "harness_type": "command", "workspace_provider": "existing-directory",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "run-task", "name": "Run task", "execution_profile_id": "run-profile", "type": "one_off",
	})
	if _, err := db.AdmitTask(t.Context(), "run-detail", "run-task", "codex-main", "revision", apiNow); err != nil {
		t.Fatal(err)
	}
	var run domain.Run
	getJSON(t, server.URL+"/v1/runs/run-detail", &run)
	if run.ID != "run-detail" || run.TaskID != "run-task" {
		t.Fatalf("run = %#v", run)
	}
	requestStatus(t, http.MethodGet, server.URL+"/v1/runs/missing", "", http.StatusNotFound)
}

func TestCapacityEndpointCorrelatesStoredLogsAndSnapshots(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, claudePayload) }))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	shortReset, weeklyReset := apiNow.Add(5*time.Hour), apiNow.Add(5*24*time.Hour)
	for index, remaining := range []float64{1, .98, .95} {
		snapshot := decision.UsageSnapshot{Provider: "claude", ObservedAt: apiNow.Add(time.Duration(index) * time.Minute),
			Short:  &decision.UsageWindow{Remaining: remaining, ResetsAt: shortReset},
			Weekly: decision.UsageWindow{Remaining: .80 - float64(index)*.01, ResetsAt: weeklyReset}, Source: "test"}
		if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.SaveTokenObservations(t.Context(), []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost", SourceID: "s:1", ObservedAt: apiNow.Add(30 * time.Second), InputTokens: 1000},
		{Provider: "claude", Source: "gatepost", SourceID: "s:2", ObservedAt: apiNow.Add(90 * time.Second), InputTokens: 4000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(testConfig(usage.URL), db, func() time.Time { return apiNow.Add(time.Hour) }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/providers/claude-main/capacity")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got capacity.EstimateResult
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Short == nil || math.Abs(got.Short.EstimatedTokens.Total-100_000) > .01 || got.Weekly == nil || math.Abs(got.Weekly.EstimatedTokens.Total-250_000) > .01 {
		t.Fatalf("capacity = %#v", got)
	}
	if got.Short.AttributionCoverage != 1 || len(got.Short.Sources) != 1 || got.Short.Sources[0].Key != "gatepost" ||
		got.RatioDerivedDifference == nil {
		t.Fatalf("capacity evidence = %#v", got)
	}
}

func TestTokenSyncIncludesExplicitPiSubscriptionProvider(t *testing.T) {
	directory := t.TempDir()
	viewerPath := filepath.Join(directory, "viewer.db")
	piPath := filepath.Join(directory, "pi.jsonl")
	if err := os.WriteFile(piPath, []byte(`{"type":"message","id":"m1","timestamp":"2026-07-16T18:00:01Z","message":{"role":"assistant","provider":"anthropic-cli","model":"claude-opus","usage":{"input":10,"output":2,"cacheRead":30,"cacheWrite":4}}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	viewer, err := sql.Open("sqlite", viewerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = viewer.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL, source_path TEXT, started_at INTEGER, ended_at INTEGER);
CREATE TABLE messages (session_id TEXT, ordinal INTEGER, role TEXT, ts INTEGER, model TEXT, context_tokens INTEGER, output_tokens INTEGER);
INSERT INTO sessions VALUES ('pi:s1', 'pi', ?, 1784224800000, 1784224810000);`, piPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer.Close()
	db, err := store.Open(filepath.Join(directory, "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://127.0.0.1:1")
	cfg.UsageMonitor.GatepostDatabase = viewerPath
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	postJSON[map[string]any](t, server.URL+"/v1/providers/claude-main/token-sync", map[string]any{})
	observations, err := db.ListTokenObservations(t.Context(), "claude", time.Time{}, time.Time{})
	if err != nil || len(observations) != 1 || observations[0].Source != "gatepost-pi" || observations[0].CacheReadTokens != 30 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
}

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

func TestTaskCRUDOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "codex-devx", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "editable", "name": "Before", "priority": 10, "execution_profile_id": "codex-devx", "type": "one_off",
	})

	request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/tasks/editable", strings.NewReader(`{
		"name":"After", "prompt":"Do useful work", "priority":80, "type":"recurring",
		"min_interval":"2d", "require_repo_change":true, "dispatch_tier":"well_behind"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var updated domain.Task
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || updated.Name != "After" || updated.Prompt != "Do useful work" || updated.MinInterval != 48*time.Hour || !updated.RequireRepoChange || updated.DispatchTier != domain.DispatchWellBehind {
		t.Fatalf("status=%d task=%#v", response.StatusCode, updated)
	}

	response, err = http.Get(server.URL + "/v1/tasks/editable")
	if err != nil {
		t.Fatal(err)
	}
	var fetched domain.Task
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || fetched.Priority != 80 || fetched.Type != domain.Recurring {
		t.Fatalf("status=%d task=%#v", response.StatusCode, fetched)
	}

	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/v1/tasks/editable", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", response.StatusCode)
	}
	response, err = http.Get(server.URL + "/v1/tasks/editable")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted GET status = %d", response.StatusCode)
	}
}

func TestExecutionProfileCRUDOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "editable-profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/profiles/editable-profile", strings.NewReader(`{
		"model":"gpt-5.4-mini", "workspace_provider":"git-worktree", "repository":"/repo"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var updated domain.ExecutionProfile
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || updated.Model != "gpt-5.4-mini" || updated.WorkspaceProvider != "git-worktree" || updated.Repository != "/repo" {
		t.Fatalf("status=%d profile=%#v", response.StatusCode, updated)
	}
	response, err = http.Get(server.URL + "/v1/profiles/editable-profile")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodDelete, server.URL+"/v1/profiles/editable-profile", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", response.StatusCode)
	}
}

func TestListAndTaskControlEndpoints(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "listed-profile", "provider_account_id": "codex-main", "harness_type": "pi", "model": "openai-codex/gpt-5.6-sol", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "controlled-task", "name": "Controlled", "priority": 42, "execution_profile_id": "listed-profile", "type": "one_off",
	})

	var profiles []domain.ExecutionProfile
	getJSON(t, server.URL+"/v1/profiles", &profiles)
	if len(profiles) != 1 || profiles[0].ID != "listed-profile" || profiles[0].HarnessType != "pi" {
		t.Fatalf("profiles = %#v", profiles)
	}
	var tasks []domain.Task
	getJSON(t, server.URL+"/v1/tasks", &tasks)
	if len(tasks) != 1 || tasks[0].ID != "controlled-task" {
		t.Fatalf("tasks = %#v", tasks)
	}

	disabled := postJSON[domain.Task](t, server.URL+"/v1/tasks/controlled-task/disable", map[string]any{})
	if disabled.Enabled || disabled.State != domain.Disabled {
		t.Fatalf("disabled = %#v", disabled)
	}
	enabled := postJSON[domain.Task](t, server.URL+"/v1/tasks/controlled-task/enable", map[string]any{})
	if !enabled.Enabled || enabled.State != domain.Queued {
		t.Fatalf("enabled = %#v", enabled)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks/missing/disable", `{}`, http.StatusNotFound)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks/controlled-task/archive", `{}`, http.StatusInternalServerError)
}

func TestProfileAndTaskValidationErrorsOverServiceAPI(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	requestStatus(t, http.MethodPost, server.URL+"/v1/profiles", `{`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/profiles", `{"id":"bad","provider_account_id":"missing","harness_type":"pi","workspace_provider":"devx"}`, http.StatusBadRequest)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "valid", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/valid", `{"provider_account_id":"missing"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks", `{`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks", `{"id":"bad-duration","name":"Bad","execution_profile_id":"valid","type":"recurring","min_interval":"never"}`, http.StatusBadRequest)
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "valid-task", "name": "Valid", "execution_profile_id": "valid", "type": "one_off",
	})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/valid-task", `{"min_interval":"never"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/valid-task", `{"execution_profile_id":"missing"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodDelete, server.URL+"/v1/profiles/valid", ``, http.StatusConflict)
}

func TestTaskAPIRoundTripsDispatchTier(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	task := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "filler", "name": "Filler", "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "expiring",
	})
	if task.DispatchTier != domain.DispatchExpiring {
		t.Fatalf("task = %#v", task)
	}
}

func TestSchedulerUnlocksJobsByDispatchTierBeforeApplyingPriority(t *testing.T) {
	payload := `{"providerId":"codex","fetchedAt":"2026-07-16T18:00:00Z","lines":[{"type":"progress","label":"Weekly","used":50,"limit":100,"resetsAt":"2026-07-18T18:00:00Z"}]}`
	server, _ := newAPIServer(t, payload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "devx",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "deep-filler", "name": "Deep filler", "priority": 100, "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "expiring",
	})
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "useful-work", "name": "Useful work", "priority": 20, "execution_profile_id": "profile", "type": "one_off", "dispatch_tier": "behind",
	})
	response := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if response.Result.UnlockedTier != domain.DispatchWellBehind || response.SelectedTask == nil || response.SelectedTask.ID != "useful-work" {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Result.CandidateRejections) == 0 || response.Result.CandidateRejections[0].TaskID != "deep-filler" {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerExplainsRecurringTaskCooldown(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	lastCompleted := apiNow.Add(-time.Hour)
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "cooling-down", Name: "Cooling down", Priority: 100, ExecutionProfileID: "profile",
		Type: domain.Recurring, MinInterval: 24 * time.Hour, LastCompletedAt: &lastCompleted,
	}, apiNow); err != nil {
		t.Fatal(err)
	}

	response := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if response.Result.Decision != decision.Run || response.SelectedTask != nil {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Result.CandidateRejections) != 1 ||
		response.Result.CandidateRejections[0].TaskID != "cooling-down" ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "cooldown until") {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerExplainsRepositoryRevisionFailure(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	profile := domain.ExecutionProfile{
		ID: "missing-repo", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "git-worktree", Repository: "/missing/repository",
	}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "repo-task", Name: "Repository task", Priority: 100, ExecutionProfileID: profile.ID,
		Type: domain.Recurring, RequireRepoChange: true,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, fakeRevisionResolver{failures: map[string]error{"missing-repo": fmt.Errorf("repository is unavailable")}})
	server := httptest.NewServer(handler)
	defer server.Close()

	response := postJSON[struct {
		Result decision.Result `json:"result"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if len(response.Result.CandidateRejections) != 1 ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "repository revision could not be read") ||
		!strings.Contains(response.Result.CandidateRejections[0].Reason, "repository is unavailable") {
		t.Fatalf("rejections = %#v", response.Result.CandidateRejections)
	}
}

func TestSchedulerSkipsExhaustedFableAndSelectsOpus(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(0, 1.0, 23*time.Hour))
	createClaudeCandidate(t, db, "fable-profile", "fable", "fable", "fable-task", 100)
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 50)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "opus-task" {
		t.Fatalf("selected task = %#v", result.SelectedTask)
	}
	if len(result.Result.CandidateRejections) == 0 ||
		result.Result.CandidateRejections[0].TaskID != "fable-task" ||
		!strings.Contains(result.Result.CandidateRejections[0].Reason, "exhausted") {
		t.Fatalf("rejections = %#v", result.Result.CandidateRejections)
	}
	if strings.Join(result.Result.RequiredPools, ",") != "session,weekly" {
		t.Fatalf("required pools = %#v", result.Result.RequiredPools)
	}
}

func TestFablePaceSignalSelectsOnlyFableTask(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(.60, .40, 48*time.Hour))
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 100)
	createClaudeCandidate(t, db, "fable-profile", "claude-fable-5", "", "fable-task", 50)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "fable-task" {
		t.Fatalf("selected task = %#v result=%#v", result.SelectedTask, result.Result)
	}
	if result.Result.Decision != decision.Run ||
		strings.Join(result.Result.TriggeringPools, ",") != "model:fable:weekly" {
		t.Fatalf("result = %#v", result.Result)
	}
	if strings.Join(result.Result.RequiredPools, ",") != "session,weekly,model:fable:weekly" {
		t.Fatalf("required pools = %#v", result.Result.RequiredPools)
	}
}

func TestFableSignalDoesNotReleaseOpusTask(t *testing.T) {
	server, db := newAPIServer(t, claudeAllowancePayload(.60, .40, 48*time.Hour))
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 100)

	result := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if result.SelectedTask != nil || result.Result.Decision != decision.Wait {
		t.Fatalf("selected=%#v result=%#v", result.SelectedTask, result.Result)
	}
}

func TestExecutionSkipsExhaustedFableAndAdmitsOpus(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, claudeAllowancePayload(0, 1.0, 23*time.Hour))
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createClaudeCandidate(t, db, "fable-profile", "fable", "fable", "fable-task", 100)
	createClaudeCandidate(t, db, "opus-profile", "opus", "", "opus-task", 50)
	executed := make(chan domain.Task, 1)
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(_ context.Context, _ domain.Run, task domain.Task, _ domain.ExecutionProfile) error {
			executed <- task
			return nil
		},
	}, fakeRevisionResolver{})
	server := httptest.NewServer(handler)
	defer server.Close()

	result := postJSON[struct {
		Result decision.Result `json:"result"`
		Run    *domain.Run     `json:"run,omitempty"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "claude-main"})
	if result.Run == nil || result.Run.TaskID != "opus-task" {
		t.Fatalf("run=%#v result=%#v", result.Run, result.Result)
	}
	select {
	case task := <-executed:
		if task.ID != "opus-task" {
			t.Fatalf("executed task = %#v", task)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not called")
	}
}

func TestSimulatedSchedulerResolvesTaskRepositoryLikeExecution(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, profile := range []domain.ExecutionProfile{
		{ID: "changed", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory", Repository: "/repo/changed"},
		{ID: "fallback", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory", Repository: "/repo/fallback"},
	} {
		if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "repo-change", Name: "Repo change", Priority: 100, ExecutionProfileID: "changed", Type: domain.Recurring, RequireRepoChange: true, LastSuccessfulSourceRevision: "old"},
		{ID: "fallback", Name: "Fallback", Priority: 10, ExecutionProfileID: "fallback", Type: domain.OneOff},
	} {
		if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, fakeRevisionResolver{revisions: map[string]string{"changed": "new", "fallback": "new"}})
	server := httptest.NewServer(handler)
	defer server.Close()

	result := postJSON[struct {
		SelectedTask *domain.Task `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "repo-change" {
		t.Fatalf("selected task = %#v, want repo-change", result.SelectedTask)
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

func TestDetailedHealthStartsHealthy(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	resp, err := http.Get(server.URL + "/v1/health/details?window=12h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var health domain.OperationalHealth
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "healthy" || health.Window != "12h0m0s" {
		t.Fatalf("health = %#v", health)
	}
}

func TestDetailedHealthRejectsInvalidWindow(t *testing.T) {
	server, _ := newAPIServer(t, claudePayload)
	for _, configured := range []string{"invalid", "0s", "-1h"} {
		resp, err := http.Get(server.URL + "/v1/health/details?window=" + url.QueryEscape(configured))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("window %q status = %d", configured, resp.StatusCode)
		}
	}
}

func TestNotificationDeliveryHistoryEndpoint(t *testing.T) {
	server, db := newAPIServer(t, claudePayload)
	id, err := db.CreateNotificationDelivery(t.Context(), domain.EventRunFailed, json.RawMessage(`{"type":"run.failed"}`), apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotificationDelivery(t.Context(), id, "failed", "offline", apiNow); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(server.URL + "/v1/notifications")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var deliveries []domain.NotificationDelivery
	if err := json.NewDecoder(resp.Body).Decode(&deliveries); err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].LastError != "offline" {
		t.Fatalf("deliveries = %#v", deliveries)
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
	if !bytes.Contains(decisions[0].DecisionJSON, []byte("repository has not changed since the last successful run")) {
		t.Fatalf("unchanged repository rejection missing from decision: %s", decisions[0].DecisionJSON)
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

func TestAutomaticSchedulerPersistsUsageFailureAttempt(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
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
	cfg.Notifications = config.Notifications{Enabled: true, Command: "true", Events: []string{domain.EventSchedulerError}}
	handler := api.NewServer(cfg, db, func() time.Time { return apiNow })
	ctx, cancel := context.WithCancel(context.Background())
	handler.StartScheduler(ctx)
	deadline := time.Now().Add(time.Second)
	var attempts []domain.DispatchAttempt
	for time.Now().Before(deadline) {
		attempts, err = db.ListDispatchAttempts(t.Context(), "codex-main", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	handler.Wait()
	if len(attempts) != 1 || attempts[0].Outcome != domain.DispatchError ||
		attempts[0].Trigger != "automatic" || !strings.Contains(attempts[0].Error, "HTTP 503") {
		t.Fatalf("attempts = %#v", attempts)
	}
	deliveries, err := db.ListNotificationDeliveries(t.Context(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "delivered" ||
		deliveries[0].EventType != domain.EventSchedulerError {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
	}
}

func TestManualExecutePersistsNoTaskAttemptAndListsIt(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/scheduler/execute", map[string]any{
		"provider_account_id": "codex-main",
	})
	resp, err := http.Get(server.URL + "/v1/scheduler/attempts?provider=codex-main")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var attempts []domain.DispatchAttempt
	if err := json.NewDecoder(resp.Body).Decode(&attempts); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Outcome != domain.DispatchNoTask || attempts[0].Trigger != "manual" ||
		attempts[0].Reason != "no queued tasks are eligible" {
		t.Fatalf("attempts = %#v", attempts)
	}
}

func TestPausedManualExecuteIsPersistedBeforeConflictResponse(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	postJSON[map[string]any](t, server.URL+"/v1/providers/codex-main/pause", map[string]any{})
	data, _ := json.Marshal(map[string]string{"provider_account_id": "codex-main"})
	resp, err := http.Post(server.URL+"/v1/scheduler/execute", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 1 || attempts[0].Outcome != domain.DispatchWait || attempts[0].Mode != "paused" {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
}

func TestRunLogsRejectArtifactOutsideConfiguredRoot(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "t", Name: "Task", ExecutionProfileID: "p", Type: domain.OneOff}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "r", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(t.Context(), "r", domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, OutputFile: outside,
	}, apiNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = root
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/runs/r/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
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

func TestRunEventsEndpointReturnsTimeline(t *testing.T) {
	server, db := newAPIServer(t, codexPayload)
	now := apiNow
	profile := domain.ExecutionProfile{ID: "events-profile", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	task := domain.Task{ID: "events-task", Name: "Events", ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "events-run", task.ID, "codex-main", "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRunEvent(t.Context(), domain.RunEvent{
		RunID: "events-run", Type: domain.RunEventStarted, OccurredAt: now,
		Payload: json.RawMessage(`{"task_id":"events-task"}`),
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(server.URL + "/v1/runs/events-run/events?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []domain.RunEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(events) != 1 || events[0].Type != domain.RunEventStarted {
		t.Fatalf("status=%d events=%#v", resp.StatusCode, events)
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

func TestCalibrationEndpointAndDecisionUseObservedWindowCost(t *testing.T) {
	server, db := newAPIServer(t, claudePayload)
	weeklyReset := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	for _, snapshot := range []decision.UsageSnapshot{
		calibrationSnapshot(apiNow.Add(-8*time.Hour), 1.00, 0.80, apiNow.Add(-6*time.Hour), weeklyReset),
		calibrationSnapshot(apiNow.Add(-7*time.Hour), 0.40, 0.75, apiNow.Add(-6*time.Hour), weeklyReset.Add(300*time.Millisecond)),
		calibrationSnapshot(apiNow.Add(-3*time.Hour), 0.90, 0.75, apiNow.Add(-time.Hour), weeklyReset),
		calibrationSnapshot(apiNow.Add(-2*time.Hour), 0.40, 0.71, apiNow.Add(-time.Hour), weeklyReset.Add(-300*time.Millisecond)),
	} {
		if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(server.URL + "/v1/providers/claude-main/calibration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var estimate calibration.Estimate
	if err := json.NewDecoder(resp.Body).Decode(&estimate); err != nil {
		t.Fatal(err)
	}
	if estimate.Source != calibration.SourceObserved || estimate.Confidence != calibration.ConfidenceMedium {
		t.Fatalf("estimate = %#v", estimate)
	}

	result := postJSON[struct {
		Result decision.Result `json:"result"`
	}](t, server.URL+"/v1/providers/claude-main/decision", map[string]any{})
	if result.Result.WindowWeeklyCostSource != string(calibration.SourceObserved) ||
		result.Result.CalibrationConfidence != string(calibration.ConfidenceMedium) ||
		result.Result.WindowWeeklyCost == 0.08 {
		t.Fatalf("result = %#v", result.Result)
	}
}

func calibrationSnapshot(observed time.Time, shortRemaining, weeklyRemaining float64, shortReset, weeklyReset time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider: "claude", ObservedAt: observed,
		Short:  &decision.UsageWindow{Remaining: shortRemaining, ResetsAt: shortReset},
		Weekly: decision.UsageWindow{Remaining: weeklyRemaining, ResetsAt: weeklyReset},
		Source: "test", Confidence: "high",
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

func TestConcurrentExecuteContentionIsWaitNotError(t *testing.T) {
	var usageRequests atomic.Int32
	usageBarrier := make(chan struct{})
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if usageRequests.Add(1) == 2 {
			close(usageBarrier)
		}
		select {
		case <-usageBarrier:
		case <-time.After(time.Second):
			t.Error("concurrent usage requests did not reach barrier")
		}
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	releaseExecutor := make(chan struct{})
	handler := api.NewServerWithExecutor(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
			<-releaseExecutor
			return nil
		},
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "profile", "provider_account_id": "codex-main",
		"harness_type": "codex-cli", "workspace_provider": "devx",
	})
	for _, id := range []string{"first", "second"} {
		postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
			"id": id, "name": id, "priority": 50,
			"execution_profile_id": "profile", "type": "one_off",
		})
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			body := bytes.NewBufferString(`{"provider_account_id":"codex-main"}`)
			resp, requestErr := http.Post(server.URL+"/v1/scheduler/execute", "application/json", body)
			if requestErr != nil {
				statuses <- 0
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	close(start)
	gotStatuses := []int{<-statuses, <-statuses}
	close(releaseExecutor)
	slices.Sort(gotStatuses)
	if !slices.Equal(gotStatuses, []int{http.StatusOK, http.StatusAccepted}) {
		t.Fatalf("statuses = %v", gotStatuses)
	}
	attempts, err := db.ListDispatchAttempts(t.Context(), "codex-main", 10)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	outcomes := []domain.DispatchOutcome{attempts[0].Outcome, attempts[1].Outcome}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []domain.DispatchOutcome{domain.DispatchAdmitted, domain.DispatchWait}) {
		t.Fatalf("outcomes = %v; attempts=%#v", outcomes, attempts)
	}
	for _, attempt := range attempts {
		if attempt.Outcome == domain.DispatchError {
			t.Fatalf("normal admission contention recorded as error: %#v", attempt)
		}
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
	cfg.Notifications = config.Notifications{Enabled: true, Command: "true", Events: []string{domain.EventRunCompleted}}
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
			resp, err := http.Get(server.URL + "/v1/runs/" + run.ID + "/logs?stream=stdout&tail_bytes=8")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var tail artifacts.Tail
			if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
				t.Fatal(err)
			}
			if !tail.Truncated || tail.Content != "\":true}\n" {
				t.Fatalf("tail = %#v", tail)
			}
			for _, query := range []string{"stream=combined", "stream=stdout&tail_bytes=0", "stream=stdout&tail_bytes=invalid"} {
				invalid, err := http.Get(server.URL + "/v1/runs/" + run.ID + "/logs?" + query)
				if err != nil {
					t.Fatal(err)
				}
				invalid.Body.Close()
				if invalid.StatusCode != http.StatusBadRequest {
					t.Fatalf("query %q status = %d", query, invalid.StatusCode)
				}
			}
			var deliveries []domain.NotificationDelivery
			for time.Now().Before(deadline) {
				deliveries, err = db.ListNotificationDeliveries(t.Context(), 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(deliveries) == 1 && deliveries[0].Status != "pending" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(deliveries) != 1 || deliveries[0].EventType != domain.EventRunCompleted || deliveries[0].Status != "delivered" {
				t.Fatalf("deliveries=%#v", deliveries)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not complete")
}

func TestLifecycleLogStreamUsesManagedArtifact(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	cfg := testConfig(usage.URL)
	cfg.RunArtifactsDir = root
	profile := domain.ExecutionProfile{ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"}
	task := domain.Task{ID: "t", Name: "Task", ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateProfile(t.Context(), profile, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), task, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(t.Context(), "run-lifecycle", task.ID, "codex-main", "", apiNow); err != nil {
		t.Fatal(err)
	}
	path := workspace.ArtifactPath(root, "run-lifecycle", "prepare", "stderr")
	if err := os.WriteFile(path, []byte("setup warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	resp, err := http.Get(server.URL + "/v1/runs/run-lifecycle/logs?stream=prepare_stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tail artifacts.Tail
	if err := json.NewDecoder(resp.Body).Decode(&tail); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || tail.Content != "setup warning\n" {
		t.Fatalf("status=%d tail=%#v", resp.StatusCode, tail)
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

func createClaudeCandidate(
	t *testing.T,
	db *store.DB,
	profileID, model, budgetGroup, taskID string,
	priority int,
) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: profileID, ProviderAccountID: "claude-main", HarnessType: "claude-code",
		Model: model, BudgetModelGroup: budgetGroup, WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: taskID, Name: taskID, Priority: priority, ExecutionProfileID: profileID, Type: domain.OneOff,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
}

func claudeAllowancePayload(fableRemaining, sharedRemaining float64, untilReset time.Duration) string {
	shortReset := apiNow.Add(5 * time.Hour).Format(time.RFC3339Nano)
	weeklyReset := apiNow.Add(untilReset).Format(time.RFC3339Nano)
	return fmt.Sprintf(`{
  "providerId":"claude", "fetchedAt":%q,
  "lines":[
    {"type":"progress","label":"Session","used":0,"limit":100,"periodDurationMs":18000000,"resetsAt":%q},
    {"type":"progress","label":"Weekly","used":%f,"limit":100,"periodDurationMs":604800000,"resetsAt":%q},
    {"type":"progress","label":"Fable","used":%f,"limit":100,"periodDurationMs":604800000,"resetsAt":%q}
  ]}`,
		apiNow.Format(time.RFC3339Nano), shortReset, (1-sharedRemaining)*100, weeklyReset,
		(1-fableRemaining)*100, weeklyReset)
}

type fakeExecutor struct {
	execute func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error
}

type fakeHarnessDiscoverer struct {
	catalog discovery.Catalog
	calls   atomic.Int32
}

func (f *fakeHarnessDiscoverer) Discover(context.Context) discovery.Catalog {
	f.calls.Add(1)
	return f.catalog
}

type fakeRevisionResolver struct {
	revisions map[string]string
	failures  map[string]error
}

func (f fakeRevisionResolver) Resolve(_ context.Context, profile domain.ExecutionProfile) (string, error) {
	if err := f.failures[profile.ID]; err != nil {
		return "", err
	}
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

func getJSON(t *testing.T, url string, result any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status=%d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		t.Fatal(err)
	}
}

func requestStatus(t *testing.T, method, url, body string, want int) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		contents, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, url, response.StatusCode, want, contents)
	}
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
