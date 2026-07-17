package store_test

import (
	"context"
	"path/filepath"
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

func openTaskDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
