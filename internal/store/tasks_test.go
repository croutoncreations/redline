package store_test

import (
	"context"
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

func TestExecutionProfileRoundTripsWorkspaceAndHarnessConfiguration(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	want := domain.ExecutionProfile{
		ID: "codex", ProviderAccountID: "codex-main", HarnessType: "codex-cli",
		Model: "gpt-5.3-codex", HarnessCommand: "custom", HarnessArgs: []string{"--strict-config"},
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
	if got.HarnessCommand != want.HarnessCommand || strings.Join(got.HarnessArgs, " ") != "--strict-config" ||
		strings.Join(got.WorkspaceArgs, " ") != "--target host" ||
		!got.RequireClean || got.CleanupPolicy != "on_success" {
		t.Fatalf("profile = %#v", got)
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
	if _, err := db.AdmitTask(ctx, "run-2", "second", "codex-main", "abc", now); err == nil {
		t.Fatal("expected active-provider admission conflict")
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
