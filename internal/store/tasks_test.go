package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

func TestTaskQueueSelectsHighestPriorityThenOldestEligible(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{
		ID: "codex-devx", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}
	if err := db.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}
	for _, task := range []domain.Task{
		{ID: "low", Name: "Low", Priority: 10, ExecutionProfileID: profile.ID, Type: domain.OneOff},
		{ID: "first-high", Name: "First", Priority: 90, ExecutionProfileID: profile.ID, Type: domain.OneOff},
		{ID: "second-high", Name: "Second", Priority: 90, ExecutionProfileID: profile.ID, Type: domain.OneOff},
	} {
		if err := db.CreateTask(ctx, task, now); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.NextEligibleTask(ctx, "codex-main", now, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "first-high" {
		t.Fatalf("selected %q, want first-high", got.ID)
	}
}

func TestTaskCanBeUpdatedAndDeletedBeforeItRuns(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Before", Priority: 10, ExecutionProfileID: profile.ID, Type: domain.OneOff}, now); err != nil {
		t.Fatal(err)
	}
	updated := domain.Task{ID: "job", Name: "After", Prompt: "Do the thing", Priority: 90, ExecutionProfileID: profile.ID, RuntimeJobID: "hermes-job-1", Type: domain.Recurring, MinInterval: 24 * time.Hour, RequireRepoChange: true, DispatchTier: domain.DispatchWellBehind}
	if err := db.UpdateTask(t.Context(), updated, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(t.Context(), "job")
	if err != nil || got.Name != "After" || got.Priority != 90 || got.RuntimeJobID != "hermes-job-1" || got.Type != domain.Recurring || got.MinInterval != 24*time.Hour || !got.RequireRepoChange || got.DispatchTier != domain.DispatchWellBehind || got.State != domain.Queued {
		t.Fatalf("task=%#v err=%v", got, err)
	}
	if err := db.DeleteTask(t.Context(), "job"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetTask(t.Context(), "job"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted task error = %v", err)
	}
}

func TestRunningOrReferencedTaskCannotBeDeleted(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"running", "historical", "requested"} {
		if err := db.CreateTask(t.Context(), domain.Task{ID: id, Name: id, ExecutionProfileID: profile.ID, Type: domain.OneOff}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.AdmitTask(t.Context(), "run-1", "running", "codex-main", "", now); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTask(t.Context(), "running"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("running delete error = %v", err)
	}
	if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{ProviderAccountID: "codex-main", SelectedTaskID: "historical", DecisionJSON: []byte(`{}`)}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTask(t.Context(), "historical"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("historical delete error = %v", err)
	}
	if _, err := db.RecordDispatchAttempt(t.Context(), domain.DispatchAttempt{
		ProviderAccountID: "codex-main", Trigger: "manual-task", Outcome: domain.DispatchWait,
		RequestedTaskID: "requested", StartedAt: now, CompletedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTask(t.Context(), "requested"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("requested delete error = %v", err)
	}
}

func TestExecutionProfileRoundTripsWorkspaceAndHarnessConfiguration(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	want := domain.ExecutionProfile{
		ID: "codex", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		Model: "gpt-5.3-codex", BudgetModelGroup: "premium",
		HarnessCommand: "custom", HarnessArgs: []string{"--strict-config"},
		WorkspaceProvider: "git-worktree", Repository: "/repo", BaseBranch: "main",
		WorkspaceArgs: []string{"--target", "host"},
		RequireClean:  true, CleanupPolicy: "on_success",
		PrepareCommand: "setup", FinalizeCommand: "finalize",
	}
	if err := db.CreateProfile(context.Background(), want, now); err != nil {
		t.Fatal(err)
	}
	profiles, err := db.ListProfiles(context.Background())
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles=%#v err=%v", profiles, err)
	}
	got := profiles[0]
	if got.BudgetModelGroup != "premium" || got.HarnessCommand != want.HarnessCommand || strings.Join(got.HarnessArgs, " ") != "--strict-config" ||
		strings.Join(got.WorkspaceArgs, " ") != "--target host" ||
		!got.RequireClean || got.CleanupPolicy != "on_success" {
		t.Fatalf("profile = %#v", got)
	}
}

func TestExecutionProfileCanBeUpdatedAndDeletedWhenUnused(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "editable", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	profile.Model = "gpt-5.4-mini"
	profile.WorkspaceProvider = "git-worktree"
	profile.Repository = "/repo"
	profile.HarnessArgs = []string{"--json"}
	if err := db.UpdateProfile(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProfile(t.Context(), profile.ID)
	if err != nil || got.Model != profile.Model || got.WorkspaceProvider != "git-worktree" || got.Repository != "/repo" || strings.Join(got.HarnessArgs, " ") != "--json" {
		t.Fatalf("profile=%#v err=%v", got, err)
	}
	if err := db.DeleteProfile(t.Context(), profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetProfile(t.Context(), profile.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted profile error = %v", err)
	}
}

func TestReferencedExecutionProfileCannotBeDeleted(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "in-use", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "job", Name: "Job", ExecutionProfileID: profile.ID, Type: domain.OneOff}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteProfile(t.Context(), profile.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("delete error = %v", err)
	}
}

func TestTaskQueueHonorsIntervalAndRepositoryChange(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(-time.Hour)
	task := domain.Task{
		ID: "recurring", Name: "Tests", Priority: 50,
		ExecutionProfileID: "profile", Type: domain.Recurring,
		MinInterval: 24 * time.Hour, RequireRepoChange: true,
		LastCompletedAt: &completed, LastSuccessfulSourceRevision: "abc",
	}
	if err := db.CreateTask(ctx, task, now); err != nil {
		t.Fatal(err)
	}

	if _, err := db.NextEligibleTask(ctx, "claude-main", now, "def"); err == nil {
		t.Fatal("expected interval to make task ineligible")
	}
	eligibleAt := now.Add(25 * time.Hour)
	if _, err := db.NextEligibleTask(ctx, "claude-main", eligibleAt, "abc"); err == nil {
		t.Fatal("expected unchanged revision to make task ineligible")
	}
	got, err := db.NextEligibleTask(ctx, "claude-main", eligibleAt, "def")
	if err != nil || got.ID != "recurring" {
		t.Fatalf("got %#v err=%v", got, err)
	}
}

func TestTaskQueueRoundTripsDispatchTier(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "filler", Name: "Filler", ExecutionProfileID: "profile", Type: domain.OneOff, DispatchTier: domain.DispatchExpiring}, now); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetTask(t.Context(), "filler")
	if err != nil || got.DispatchTier != domain.DispatchExpiring {
		t.Fatalf("task=%#v err=%v", got, err)
	}
}

func TestEligibleTasksLeavesRepositoryChangeEvaluationToDispatcher(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(context.Background(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "existing-directory",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), domain.Task{
		ID: "repo-task", Name: "Repo task", ExecutionProfileID: "profile", Type: domain.Recurring,
		RequireRepoChange: true, LastSuccessfulSourceRevision: "old",
	}, now); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.EligibleTasks(context.Background(), "codex-main", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "repo-task" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestSchedulerDecisionIsPersistedWithoutMutatingTask(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, domain.Task{
		ID: "task", Name: "Review", Priority: 50, ExecutionProfileID: "profile", Type: domain.OneOff,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordSchedulerDecision(ctx, domain.SchedulerDecision{
		ProviderAccountID: "codex-main", DecisionJSON: []byte(`{"decision":"RUN"}`), SelectedTaskID: "task",
	}, now); err != nil {
		t.Fatal(err)
	}
	decisions, err := db.ListSchedulerDecisions(ctx, "codex-main", 10)
	if err != nil || len(decisions) != 1 || decisions[0].SelectedTaskID != "task" {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	got, err := db.GetTask(ctx, "task")
	if err != nil || got.State != domain.Queued {
		t.Fatalf("task=%#v err=%v", got, err)
	}
}

func TestTaskControlTransitionsAndRequeuesRetryAtBottom(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, domain.Task{
		ID: "task", Name: "Review", Priority: 50, ExecutionProfileID: "profile", Type: domain.OneOff,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskControl(ctx, "task", "disable", now); err != nil {
		t.Fatal(err)
	}
	disabled, _ := db.GetTask(ctx, "task")
	if disabled.Enabled || disabled.State != domain.Disabled {
		t.Fatalf("disabled task = %#v", disabled)
	}
	if err := db.SetTaskControl(ctx, "task", "enable", now); err != nil {
		t.Fatal(err)
	}
	enabled, _ := db.GetTask(ctx, "task")
	if !enabled.Enabled || enabled.State != domain.Queued {
		t.Fatalf("enabled task = %#v", enabled)
	}
}

func TestProviderPauseStatePersists(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	paused, err := db.ProviderPaused(ctx, "codex-main")
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := db.SetProviderPaused(ctx, "codex-main", false); err != nil {
		t.Fatal(err)
	}
	paused, err = db.ProviderPaused(ctx, "codex-main")
	if err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
}

func TestProviderPolicyOverridePersistsWithoutChangingPause(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", "early"); err != nil {
		t.Fatal(err)
	}
	policy, err := db.ProviderPolicy(ctx, "codex-main")
	if err != nil || policy != "early" {
		t.Fatalf("policy=%q err=%v", policy, err)
	}
	paused, err := db.ProviderPaused(ctx, "codex-main")
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", ""); err != nil {
		t.Fatal(err)
	}
	policy, err = db.ProviderPolicy(ctx, "codex-main")
	if err != nil || policy != "" {
		t.Fatalf("cleared policy=%q err=%v", policy, err)
	}
}

func TestProviderConcurrencyOverridePersistsWithoutChangingOtherControls(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", "early"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderMaxConcurrentRuns(ctx, "codex-main", 3); err != nil {
		t.Fatal(err)
	}
	limit, err := db.ProviderMaxConcurrentRuns(ctx, "codex-main")
	if err != nil || limit != 3 {
		t.Fatalf("limit=%d err=%v", limit, err)
	}
	paused, _ := db.ProviderPaused(ctx, "codex-main")
	policy, _ := db.ProviderPolicy(ctx, "codex-main")
	if !paused || policy != "early" {
		t.Fatalf("paused=%t policy=%q", paused, policy)
	}
	if err := db.SetProviderMaxConcurrentRuns(ctx, "codex-main", 0); err != nil {
		t.Fatal(err)
	}
	limit, err = db.ProviderMaxConcurrentRuns(ctx, "codex-main")
	if err != nil || limit != 0 {
		t.Fatalf("cleared limit=%d err=%v", limit, err)
	}
}

func TestRunAdmissionIsAtomicAndOnlyOneRunPerProvider(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := db.CreateTask(ctx, domain.Task{
			ID: id, Name: id, Priority: 50, ExecutionProfileID: "profile", Type: domain.OneOff,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
	run, err := db.AdmitTask(ctx, "run-1", "first", "codex-main", "abc", now)
	if err != nil || run.State != domain.RunPreparing {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	if _, err := db.AdmitTask(ctx, "run-2", "second", "codex-main", "abc", now); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected active-provider conflict, got %v", err)
	}
	claimed, _ := db.GetTask(ctx, "first")
	if claimed.State != domain.Running {
		t.Fatalf("task state = %s", claimed.State)
	}
}

func TestConcurrentAdmissionCannotDuplicateProviderRun(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory",
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := db.CreateTask(t.Context(), domain.Task{
			ID: id, Name: id, ExecutionProfileID: "profile", Type: domain.OneOff,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index, taskID := range []string{"first", "second"} {
		workers.Add(1)
		go func(index int, taskID string) {
			defer workers.Done()
			<-start
			_, err := db.AdmitTask(context.Background(), fmt.Sprintf("run-%d", index), taskID, "codex-main", "rev", now)
			results <- err
		}(index, taskID)
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded, failed := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("admissions succeeded=%d failed=%d", succeeded, failed)
	}
	runs, err := db.ListRuns(t.Context(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestRunAdmissionEnforcesRuntimeConnectionAndAgentContextLimits(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	connection := domain.RuntimeConnection{
		ID: "hermes-remote", Runtime: "hermes", Transport: "gateway",
		URL: "https://hermes.example", MaxConcurrentRuns: 2,
	}
	if err := db.CreateRuntimeConnection(t.Context(), connection, now); err != nil {
		t.Fatal(err)
	}
	for _, contextID := range []string{"context-a", "context-b", "context-c"} {
		if err := db.CreateAgentContext(t.Context(), domain.AgentContext{
			ID: contextID, RuntimeConnectionID: connection.ID, SessionMode: "isolated", MaxConcurrentRuns: 1,
		}, now); err != nil {
			t.Fatal(err)
		}
		profileID := "profile-" + contextID
		if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
			ID: profileID, ProviderAccountID: "codex-main", AgentContextID: contextID,
			HarnessType: "hermes", WorkspaceProvider: "runtime-owned",
		}, now); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"one", "two"} {
			if err := db.CreateTask(t.Context(), domain.Task{
				ID: contextID + "-" + suffix, Name: suffix, ExecutionProfileID: profileID, Type: domain.OneOff,
			}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	limits := func(contextID string) store.AdmissionLimits {
		return store.AdmissionLimits{
			Provider: 3, RuntimeConnectionID: connection.ID, RuntimeConnection: 2,
			AgentContextID: contextID, AgentContext: 1,
		}
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-a", "context-a-one", "codex-main", "",
		nil, limits("context-a"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-a2", "context-a-two", "codex-main", "",
		nil, limits("context-a"), now); !errors.Is(err, store.ErrConflict) ||
		!strings.Contains(err.Error(), "agent context") {
		t.Fatalf("expected context conflict, got %v", err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-b", "context-b-one", "codex-main", "",
		nil, limits("context-b"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-c", "context-c-one", "codex-main", "",
		nil, limits("context-c"), now); !errors.Is(err, store.ErrConflict) ||
		!strings.Contains(err.Error(), "runtime connection") {
		t.Fatalf("expected connection conflict, got %v", err)
	}
	run, err := db.GetRun(t.Context(), "run-a")
	if err != nil || run.RuntimeConnectionID != connection.ID || run.AgentContextID != "context-a" {
		t.Fatalf("run=%#v err=%v", run, err)
	}
}

func TestConcurrentAdmissionAllowsIndependentProviders(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profiles := []domain.ExecutionProfile{
		{ID: "codex", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory"},
		{ID: "claude", ProviderAccountID: "claude-main", HarnessType: "command", WorkspaceProvider: "existing-directory"},
	}
	for _, profile := range profiles {
		if err := db.CreateProfile(t.Context(), profile, now); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateTask(t.Context(), domain.Task{
			ID: profile.ID, Name: profile.ID, ExecutionProfileID: profile.ID, Type: domain.OneOff,
		}, now); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(profiles))
	var workers sync.WaitGroup
	for _, profile := range profiles {
		workers.Add(1)
		go func(profile domain.ExecutionProfile) {
			defer workers.Done()
			<-start
			_, err := db.AdmitTask(context.Background(), "run-"+profile.ID, profile.ID, profile.ProviderAccountID, "rev", now)
			results <- err
		}(profile)
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	runs, err := db.ListRuns(t.Context(), 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestAdmissionAllowsConfiguredProviderConcurrency(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	createAdmissionTasks(t, db, now, "first", "second", "third")
	limits := store.AdmissionLimits{Provider: 2}
	for index, taskID := range []string{"first", "second"} {
		if _, err := db.AdmitTaskWithLimits(t.Context(), fmt.Sprintf("run-%d", index), taskID,
			"codex-main", "rev", []string{"weekly"}, limits, now); err != nil {
			t.Fatalf("admit %s: %v", taskID, err)
		}
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-3", "third", "codex-main", "rev",
		[]string{"weekly"}, limits, now); !errors.Is(err, store.ErrConflict) ||
		!strings.Contains(err.Error(), "provider concurrency limit 2") {
		t.Fatalf("expected provider concurrency conflict, got %v", err)
	}
}

func TestAdmissionEnforcesOnlyConfiguredPoolLimits(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	createAdmissionTasks(t, db, now, "fable-one", "fable-two", "opus")
	limits := store.AdmissionLimits{Provider: 3, Pools: map[string]int{"model:fable:weekly": 1}}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-fable-one", "fable-one", "claude-main", "rev",
		[]string{"session", "weekly", "model:fable:weekly"}, limits, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-fable-two", "fable-two", "claude-main", "rev",
		[]string{"session", "weekly", "model:fable:weekly"}, limits, now); !errors.Is(err, store.ErrConflict) ||
		!strings.Contains(err.Error(), `allowance pool "model:fable:weekly" concurrency limit 1`) {
		t.Fatalf("expected Fable pool conflict, got %v", err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-opus", "opus", "claude-main", "rev",
		[]string{"session", "weekly"}, limits, now); err != nil {
		t.Fatalf("unlimited shared pools should admit Opus: %v", err)
	}
}

func TestConcurrentAdmissionHonorsConfiguredProviderLimit(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	createAdmissionTasks(t, db, now, "first", "second", "third")
	start := make(chan struct{})
	results := make(chan error, 3)
	var workers sync.WaitGroup
	for index, taskID := range []string{"first", "second", "third"} {
		workers.Add(1)
		go func(index int, taskID string) {
			defer workers.Done()
			<-start
			_, err := db.AdmitTaskWithLimits(context.Background(), fmt.Sprintf("parallel-%d", index),
				taskID, "codex-main", "rev", []string{"weekly"}, store.AdmissionLimits{Provider: 2}, now)
			results <- err
		}(index, taskID)
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, store.ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if succeeded != 2 || conflicted != 1 {
		t.Fatalf("admissions succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestConcurrentAdmissionHonorsConfiguredPoolLimit(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	createAdmissionTasks(t, db, now, "fable-one", "fable-two", "opus")
	start := make(chan struct{})
	type admission struct {
		taskID string
		err    error
	}
	results := make(chan admission, 3)
	var workers sync.WaitGroup
	for index, candidate := range []struct {
		taskID string
		pools  []string
	}{
		{taskID: "fable-one", pools: []string{"weekly", "model:fable:weekly"}},
		{taskID: "fable-two", pools: []string{"weekly", "model:fable:weekly"}},
		{taskID: "opus", pools: []string{"weekly"}},
	} {
		workers.Add(1)
		go func(index int, candidate struct {
			taskID string
			pools  []string
		}) {
			defer workers.Done()
			<-start
			_, err := db.AdmitTaskWithLimits(context.Background(), fmt.Sprintf("pool-parallel-%d", index),
				candidate.taskID, "claude-main", "rev", candidate.pools,
				store.AdmissionLimits{Provider: 3, Pools: map[string]int{"model:fable:weekly": 1}}, now)
			results <- admission{taskID: candidate.taskID, err: err}
		}(index, candidate)
	}
	close(start)
	workers.Wait()
	close(results)
	fableSucceeded, fableConflicted, opusSucceeded := 0, 0, false
	for result := range results {
		switch {
		case result.taskID == "opus" && result.err == nil:
			opusSucceeded = true
		case strings.HasPrefix(result.taskID, "fable") && result.err == nil:
			fableSucceeded++
		case strings.HasPrefix(result.taskID, "fable") && errors.Is(result.err, store.ErrConflict):
			fableConflicted++
		default:
			t.Fatalf("unexpected admission result: %#v", result)
		}
	}
	if fableSucceeded != 1 || fableConflicted != 1 || !opusSucceeded {
		t.Fatalf("fable succeeded=%d conflicted=%d opus_succeeded=%t",
			fableSucceeded, fableConflicted, opusSucceeded)
	}
}

func createAdmissionTasks(t *testing.T, db *store.DB, now time.Time, taskIDs ...string) {
	t.Helper()
	provider := "codex-main"
	if strings.HasPrefix(taskIDs[0], "fable") {
		provider = "claude-main"
	}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "concurrent-profile", ProviderAccountID: provider, HarnessType: "command",
		WorkspaceProvider: "existing-directory",
	}, now); err != nil {
		t.Fatal(err)
	}
	for _, taskID := range taskIDs {
		if err := db.CreateTask(t.Context(), domain.Task{
			ID: taskID, Name: taskID, ExecutionProfileID: "concurrent-profile", Type: domain.OneOff,
		}, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHasActiveRun(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(context.Background(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		WorkspaceProvider: "existing-directory",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), domain.Task{
		ID: "task", Name: "Task", ExecutionProfileID: "profile", Type: domain.OneOff,
	}, now); err != nil {
		t.Fatal(err)
	}
	active, err := db.HasActiveRun(context.Background(), "codex-main")
	if err != nil || active {
		t.Fatalf("before admission active=%v err=%v", active, err)
	}
	if _, err := db.AdmitTask(context.Background(), "run", "task", "codex-main", "rev", now); err != nil {
		t.Fatal(err)
	}
	active, err = db.HasActiveRun(context.Background(), "codex-main")
	if err != nil || !active {
		t.Fatalf("after admission active=%v err=%v", active, err)
	}
}

func TestSuccessfulRecurringRunRequeuesAtBottom(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, domain.Task{
		ID: "repeat", Name: "repeat", Priority: 50, ExecutionProfileID: "profile", Type: domain.Recurring,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(ctx, "run-1", "repeat", "claude-main", "abc", now); err != nil {
		t.Fatal(err)
	}
	workspace := domain.Workspace{Directory: "/tmp/work", SessionID: "session"}
	if err := db.MarkRunRunning(ctx, "run-1", workspace); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(ctx, "run-1", domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, OutputFile: "/tmp/out", FinalizeState: "completed",
		Summary: "Added regression coverage.", Outcome: "changes_proposed",
		Artifacts:      []domain.RunArtifact{{Type: "pull_request", Label: "PR #42", URL: "https://github.com/example/repo/pull/42"}},
		ActualProvider: "anthropic", ActualModel: "claude-opus-4-1",
	}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	task, _ := db.GetTask(ctx, "repeat")
	if task.State != domain.Queued || task.LastCompletedAt == nil || task.LastSuccessfulSourceRevision != "abc" {
		t.Fatalf("task = %#v", task)
	}
	run, err := db.GetRun(ctx, "run-1")
	if err != nil || run.State != domain.RunCompleted || run.Workspace.SessionID != "session" {
		t.Fatalf("run=%#v err=%v", run, err)
	}
	if run.Summary != "Added regression coverage." || run.Outcome != "changes_proposed" ||
		len(run.Artifacts) != 1 || run.ActualProvider != "anthropic" ||
		run.ActualModel != "claude-opus-4-1" || run.ActivityReadAt != nil {
		t.Fatalf("activity = %#v", run)
	}
	readAt := now.Add(2 * time.Hour)
	if err := db.MarkRunActivityRead(ctx, run.ID, readAt); err != nil {
		t.Fatal(err)
	}
	run, err = db.GetRun(ctx, run.ID)
	if err != nil || run.ActivityReadAt == nil || !run.ActivityReadAt.Equal(readAt) {
		t.Fatalf("marked read run=%#v err=%v", run, err)
	}
}

func TestRecoverInterruptedRunsMarksRunAndTaskFailed(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(ctx, domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, domain.Task{
		ID: "task", Name: "task", Priority: 50, ExecutionProfileID: "profile", Type: domain.OneOff,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(ctx, "run", "task", "codex-main", "abc", now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverInterruptedRuns(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, _ := db.GetRun(ctx, "run")
	task, _ := db.GetTask(ctx, "task")
	if run.State != domain.RunFailed || task.State != domain.Failed {
		t.Fatalf("run=%s task=%s", run.State, task.State)
	}
}

func TestRestartRecoveryPreservesWorkspaceAndIsIdempotent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "redline.db")
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []domain.ExecutionProfile{
		{ID: "codex", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx"},
		{ID: "claude", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "devx"},
	} {
		if err := db.CreateProfile(t.Context(), profile, now); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateTask(t.Context(), domain.Task{
			ID: profile.ID, Name: profile.ID, ExecutionProfileID: profile.ID, Type: domain.OneOff,
		}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(t.Context(), "run-"+profile.ID, profile.ID, profile.ProviderAccountID, "rev", now); err != nil {
			t.Fatal(err)
		}
	}
	workspace := domain.Workspace{Directory: "/tmp/redline-worktree", SessionID: "redline-session"}
	if err := db.MarkRunRunning(t.Context(), "run-codex", workspace); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recoveredAt := now.Add(time.Minute)
	if err := db.RecoverInterruptedRuns(t.Context(), recoveredAt); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverInterruptedRuns(t.Context(), recoveredAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	running, _ := db.GetRun(t.Context(), "run-codex")
	preparing, _ := db.GetRun(t.Context(), "run-claude")
	for _, run := range []domain.Run{running, preparing} {
		task, _ := db.GetTask(t.Context(), run.TaskID)
		if run.State != domain.RunFailed || task.State != domain.Failed || run.ExitCode == nil || *run.ExitCode != -1 {
			t.Fatalf("run=%#v task=%#v", run, task)
		}
		if run.CompletedAt == nil || !run.CompletedAt.Equal(recoveredAt) {
			t.Fatalf("completion time changed during repeated recovery: %#v", run.CompletedAt)
		}
	}
	if running.Workspace.SessionID != "redline-session" || running.FinalizeState != "preserved" ||
		!strings.Contains(running.FinalizeError, "manual recovery") {
		t.Fatalf("running recovery = %#v", running)
	}
	if preparing.FinalizeState != "skipped" {
		t.Fatalf("preparing recovery = %#v", preparing)
	}
	for _, run := range []domain.Run{running, preparing} {
		events, err := db.ListRunEvents(t.Context(), run.ID, 10)
		if err != nil || len(events) != 1 || events[0].Type != domain.RunEventFailed ||
			!strings.Contains(string(events[0].Payload), `"recovery":"service_restart"`) {
			t.Fatalf("recovery events for %s = %#v err=%v", run.ID, events, err)
		}
	}
}

func openTaskDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
