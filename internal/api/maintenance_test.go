package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestTaskKindRoundTripsThroughCreateUpdateAndGet(t *testing.T) {
	t.Parallel()
	server, _ := newAPIServer(t, codexPayload)
	postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", map[string]any{
		"id": "worktree", "provider_account_id": "codex-main", "harness_type": "codex-cli",
		"workspace_provider": "git-worktree", "repository": "/repo",
	})
	created := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "maint", "name": "Maintain PRs", "execution_profile_id": "worktree",
		"kind": "pr_maintenance", "type": "recurring", "min_interval": "12h", "priority": 65,
	})
	if created.Kind != domain.KindPRMaintenance || created.Prompt != "" {
		t.Fatalf("created = %#v", created)
	}
	standard := postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "plain", "name": "Plain", "execution_profile_id": "worktree", "type": "one_off", "prompt": "x",
	})
	if standard.Kind != domain.KindStandard {
		t.Fatalf("default kind = %q", standard.Kind)
	}
	var updated domain.Task
	patchJSON(t, server.URL+"/v1/tasks/plain", map[string]any{"kind": "pr_maintenance", "type": "recurring", "min_interval": "1d"}, &updated)
	if updated.Kind != domain.KindPRMaintenance {
		t.Fatalf("updated = %#v", updated)
	}
	var fetched domain.Task
	getJSON(t, server.URL+"/v1/tasks/plain", &fetched)
	if fetched.Kind != domain.KindPRMaintenance {
		t.Fatalf("fetched = %#v", fetched)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks",
		`{"id":"bad","name":"Bad","execution_profile_id":"worktree","kind":"pr_maintenance","type":"one_off"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks",
		`{"id":"bad","name":"Bad","execution_profile_id":"worktree","kind":"weekly","type":"one_off"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/maint", `{"min_interval":""}`, http.StatusBadRequest)

	var dashboard struct {
		Tasks []struct {
			ID   string          `json:"id"`
			Kind domain.TaskKind `json:"kind"`
		} `json:"tasks"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	kinds := map[string]domain.TaskKind{}
	for _, task := range dashboard.Tasks {
		kinds[task.ID] = task.Kind
	}
	if kinds["maint"] != domain.KindPRMaintenance {
		t.Fatalf("dashboard kinds = %#v", kinds)
	}
}

func TestAttributionAndBranchLeaseEndpoints(t *testing.T) {
	t.Parallel()
	server, db := newAPIServer(t, codexPayload)
	ctx := t.Context()
	repo := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	for _, profile := range []domain.ExecutionProfile{
		{ID: "std", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: repo},
		{ID: "maint", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "git-worktree", Repository: repo},
	} {
		if err := db.CreateProfile(ctx, profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "bug-hunt", Name: "Bug hunt", ExecutionProfileID: "std", Type: domain.OneOff},
		{ID: "maint", Name: "Maintain", Kind: domain.KindPRMaintenance, ExecutionProfileID: "maint", Type: domain.Recurring, MinInterval: 1e9},
	} {
		if err := db.CreateTask(ctx, task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	start := func(runID, taskID, provider string) {
		t.Helper()
		if _, err := db.AdmitTaskWithLimits(ctx, runID, taskID, provider, "", nil, store.AdmissionLimits{Provider: 5}, apiNow); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkRunRunning(ctx, runID, domain.Workspace{
			Directory: filepath.Join(repo, ".redline", "worktrees", runID), Branch: "redline/" + runID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	start("origin-run", "bug-hunt", "codex-main")
	attributionURL := server.URL + "/v1/runs/attribution?branch=" + url.QueryEscape("redline/origin-run") + "&repository=" + url.QueryEscape(repo)
	var attribution domain.RunAttribution
	getJSON(t, attributionURL, &attribution)
	if attribution.Attributable || attribution.RunState != domain.RunRunning {
		t.Fatalf("active attribution = %#v", attribution)
	}
	if err := db.CompleteRun(ctx, "origin-run", domain.RunCompletion{State: domain.RunCompleted}, apiNow); err != nil {
		t.Fatal(err)
	}
	getJSON(t, attributionURL, &attribution)
	if !attribution.Attributable || attribution.TaskName != "Bug hunt" || attribution.TaskKind != domain.KindStandard {
		t.Fatalf("attribution = %#v", attribution)
	}
	requestStatus(t, http.MethodGet, server.URL+"/v1/runs/attribution?branch=redline/origin-run", "", http.StatusBadRequest)

	start("maint-run", "maint", "claude-main")
	body, _ := json.Marshal(map[string]string{"repository": repo, "branch": "redline/origin-run", "run_id": "maint-run"})
	lease := postJSON[domain.BranchLease](t, server.URL+"/v1/branch-leases", json.RawMessage(body))
	if lease.RunID != "maint-run" {
		t.Fatalf("lease = %#v", lease)
	}
	requestStatus(t, http.MethodPost, server.URL+"/v1/branch-leases",
		`{"repository":"`+repo+`","branch":"redline/origin-run","run_id":"origin-run"}`, http.StatusConflict)
	requestStatus(t, http.MethodPost, server.URL+"/v1/branch-leases",
		`{"repository":"`+repo+`","branch":"redline/origin-run","run_id":"missing"}`, http.StatusNotFound)
	release := server.URL + "/v1/branch-leases?repository=" + url.QueryEscape(repo) + "&branch=" + url.QueryEscape("redline/origin-run")
	requestStatus(t, http.MethodDelete, release+"&run_id=origin-run", "", http.StatusConflict)
	requestStatus(t, http.MethodDelete, release+"&run_id=maint-run", "", http.StatusNoContent)
	requestStatus(t, http.MethodDelete, release+"&run_id=maint-run", "", http.StatusNotFound)
}

func TestSchedulerSkipsMaintenanceTaskWhileRepositoryMaintenanceIsActive(t *testing.T) {
	t.Parallel()
	server, db := newAPIServer(t, codexPayload)
	ctx := t.Context()
	for _, profile := range []domain.ExecutionProfile{
		{ID: "maint-a", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo"},
		{ID: "maint-b", ProviderAccountID: "codex-main", HarnessType: "claude-code", WorkspaceProvider: "devx", Repository: "/repo"},
	} {
		if err := db.CreateProfile(ctx, profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []domain.Task{
		{ID: "first", Name: "First", Kind: domain.KindPRMaintenance, ExecutionProfileID: "maint-a", Type: domain.Recurring, MinInterval: 1e9, Priority: 10},
		{ID: "second", Name: "Second", Kind: domain.KindPRMaintenance, ExecutionProfileID: "maint-b", Type: domain.Recurring, MinInterval: 1e9, Priority: 90},
	} {
		if err := db.CreateTask(ctx, task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.AdmitTaskWithLimits(ctx, "running", "first", "codex-main", "", nil, store.AdmissionLimits{Provider: 5}, apiNow); err != nil {
		t.Fatal(err)
	}
	response := postJSON[struct {
		Result       decision.Result `json:"result"`
		SelectedTask *domain.Task    `json:"selected_task,omitempty"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "codex-main"})
	if response.SelectedTask != nil {
		t.Fatalf("selected %#v while repository maintenance is active", response.SelectedTask)
	}
	found := false
	for _, rejection := range response.Result.CandidateRejections {
		if rejection.TaskID == "second" && strings.Contains(rejection.Reason, "pr_maintenance run running is already active") {
			found = true
		}
	}
	if !found {
		t.Fatalf("decision=%s reason=%q rejections=%#v", response.Result.Decision, response.Result.Reason, response.Result.CandidateRejections)
	}
}

// An incompatible profile is a client error, not a server error.
func TestPRMaintenanceProfileValidationReturnsBadRequest(t *testing.T) {
	t.Parallel()
	server, _ := newAPIServer(t, codexPayload)
	connection := postJSON[domain.RuntimeConnection](t, server.URL+"/v1/runtime-connections", map[string]any{
		"id": "hermes-remote", "runtime": "hermes", "transport": "gateway", "url": "https://hermes.example",
	})
	agentContext := postJSON[domain.AgentContext](t, server.URL+"/v1/agent-contexts", map[string]any{
		"id": "hermes-context", "runtime_connection_id": connection.ID, "session_mode": "isolated",
	})
	for _, profile := range []map[string]any{
		{"id": "worktree", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "git-worktree", "repository": "/repo"},
		{"id": "hermes", "provider_account_id": "codex-main", "harness_type": "hermes", "agent_context_id": agentContext.ID, "workspace_provider": "git-worktree", "repository": "/repo"},
		{"id": "norepo", "provider_account_id": "codex-main", "harness_type": "codex-cli", "workspace_provider": "git-worktree"},
	} {
		postJSON[domain.ExecutionProfile](t, server.URL+"/v1/profiles", profile)
	}
	postJSON[domain.Task](t, server.URL+"/v1/tasks", map[string]any{
		"id": "maint", "name": "Maintain", "execution_profile_id": "worktree",
		"kind": "pr_maintenance", "type": "recurring", "min_interval": "12h",
	})
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/maint", `{"execution_profile_id":"hermes"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/tasks/maint", `{"execution_profile_id":"norepo"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/tasks",
		`{"id":"m2","name":"M2","execution_profile_id":"norepo","kind":"pr_maintenance","type":"recurring","min_interval":"1h"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/worktree", `{"harness_type":"hermes","agent_context_id":"hermes-context"}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPatch, server.URL+"/v1/profiles/worktree", `{"repository":""}`, http.StatusBadRequest)
}

// racingResolver admits a competing maintenance run while the scheduler is
// between its "already active" check and admission.
type racingResolver struct{ race func() }

func (r racingResolver) Resolve(context.Context, domain.ExecutionProfile) (string, error) {
	r.race()
	return "new", nil
}

func TestSchedulerExplainsMaintenanceAdmissionConflict(t *testing.T) {
	t.Parallel()
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, codexPayload)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	for _, profile := range []domain.ExecutionProfile{
		{ID: "maint-a", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: "/repo"},
		{ID: "maint-b", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "devx", Repository: "/repo"},
	} {
		if err := db.CreateProfile(ctx, profile, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	gated := domain.Task{ID: "gated", Name: "Gated", Kind: domain.KindPRMaintenance, ExecutionProfileID: "maint-a",
		Type: domain.Recurring, MinInterval: 1e9, Priority: 90, RequireRepoChange: true}
	rival := domain.Task{ID: "rival", Name: "Rival", Kind: domain.KindPRMaintenance, ExecutionProfileID: "maint-b",
		Type: domain.Recurring, MinInterval: 1e9}
	for _, task := range []domain.Task{gated, rival} {
		if err := db.CreateTask(ctx, task, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	handler := api.NewServerWithDependencies(testConfig(usage.URL), db, func() time.Time { return apiNow }, fakeExecutor{
		execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil },
	}, racingResolver{race: func() {
		if _, err := db.AdmitTaskWithLimits(context.Background(), "rival-run", "rival", "claude-main", "", nil,
			store.AdmissionLimits{Provider: 5}, apiNow); err != nil {
			t.Error(err)
		}
	}})
	server := httptest.NewServer(handler)
	defer server.Close()
	response := postJSON[struct {
		Result decision.Result `json:"result"`
		Run    *domain.Run     `json:"run,omitempty"`
	}](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "codex-main"})
	if response.Run != nil {
		t.Fatalf("admitted %#v despite active rival", response.Run)
	}
	if response.Result.TaskSelectionReason != "another pr_maintenance run became active for this repository first" ||
		!strings.Contains(response.Result.Reason, "rival-run") {
		t.Fatalf("result = %#v", response.Result)
	}
}
