package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
)

// TestUnreadRunActivityCountAndMarkAllRead exercises MarkAllRunActivityRead and
// UnreadRunActivityCount, which were previously at 0% coverage.
//
// The bug class these guard against: a WHERE-clause error in the bulk-mark
// could silently skip some runs, leaving the unread count positive after the
// call.  A second correctness risk is that active runs (preparing/running)
// must never appear in the unread count, so the test verifies the state
// machine boundary explicitly.
func TestUnreadRunActivityCountAndMarkAllRead(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	profile := domain.ExecutionProfile{
		ID: "p-unread", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}

	// Helper to create a task and immediately admit a run for it, then
	// complete the run in the requested terminal state.
	completeRun := func(taskID, runID string, state domain.RunState) {
		t.Helper()
		task := domain.Task{
			ID:                 taskID,
			Name:               taskID,
			ExecutionProfileID: profile.ID,
			Type:               domain.OneOff,
		}
		if err := db.CreateTask(ctx, task, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(ctx, runID, taskID, "codex-main", "", now); err != nil {
			t.Fatal(err)
		}
		completion := domain.RunCompletion{State: state, FinalizeState: "completed"}
		if state == domain.RunFailed {
			completion.ExitCode = 1
			completion.Error = "simulated failure"
		}
		if err := db.CompleteRun(ctx, runID, completion, now); err != nil {
			t.Fatal(err)
		}
	}

	// Before any terminal runs the count must be zero.
	count, err := db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("initial unread count = %d, want 0", count)
	}

	// Add two completed runs and one failed run — all terminal, all unread.
	completeRun("task-c1", "run-c1", domain.RunCompleted)
	completeRun("task-c2", "run-c2", domain.RunCompleted)
	completeRun("task-f1", "run-f1", domain.RunFailed)

	count, err = db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("after 3 terminal runs: unread count = %d, want 3", count)
	}

	// Add one more task whose run is left active (preparing). Active runs
	// must never appear in the unread count.
	activeProfile := domain.ExecutionProfile{
		ID: "p-active", ProviderAccountID: "codex-active",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(ctx, activeProfile, now); err != nil {
		t.Fatal(err)
	}
	activeTask := domain.Task{
		ID:                 "task-active",
		Name:               "active task",
		ExecutionProfileID: activeProfile.ID,
		Type:               domain.OneOff,
	}
	if err := db.CreateTask(ctx, activeTask, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(ctx, "run-active", "task-active", "codex-active", "", now); err != nil {
		t.Fatal(err)
	}
	// Count must still be 3 — the active run is excluded.
	count, err = db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("after adding active run: unread count = %d, want 3", count)
	}

	// Mark all terminal runs read at once.
	if err := db.MarkAllRunActivityRead(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Count must now be zero — all three terminal runs were marked.
	count, err = db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("after MarkAllRunActivityRead: unread count = %d, want 0", count)
	}

	// Calling MarkAllRunActivityRead again must be idempotent.
	if err := db.MarkAllRunActivityRead(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	count, err = db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("after second MarkAllRunActivityRead: unread count = %d, want 0", count)
	}

	// A new terminal run after the bulk-mark must appear as unread.
	completeRun("task-c3", "run-c3", domain.RunCompleted)
	count, err = db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("after new terminal run post-mark: unread count = %d, want 1", count)
	}
}

// TestMarkRunActivityReadRequiresTerminalRun confirms that MarkRunActivityRead
// returns ErrNotFound when targeting a run that is still active, preventing
// callers from prematurely clearing the unread flag on in-progress work.
func TestMarkRunActivityReadRequiresTerminalRun(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	profile := domain.ExecutionProfile{
		ID: "p-mrar", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	if err := db.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{
		ID:                 "task-mrar",
		Name:               "mark read active",
		ExecutionProfileID: profile.ID,
		Type:               domain.OneOff,
	}
	if err := db.CreateTask(ctx, task, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(ctx, "run-mrar", "task-mrar", "codex-main", "", now); err != nil {
		t.Fatal(err)
	}

	// MarkRunActivityRead on an active (preparing) run must return ErrNotFound.
	err := db.MarkRunActivityRead(ctx, "run-mrar", now)
	if err == nil {
		t.Fatal("expected ErrNotFound marking active run as read, got nil")
	}

	// Now complete the run and verify the per-run mark succeeds.
	if err := db.CompleteRun(ctx, "run-mrar", domain.RunCompletion{
		State: domain.RunCompleted, FinalizeState: "completed",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRunActivityRead(ctx, "run-mrar", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The single-run mark must suppress the run from the unread count.
	count, err := db.UnreadRunActivityCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("after single-run mark: unread count = %d, want 0", count)
	}
}

// TestListRunsOrdersChronologicallyAcrossFractionalSecondWidths guards against
// a real ordering bug: run timestamps are persisted as TEXT via formatTime,
// which formats with time.RFC3339Nano. That layout trims trailing zeros from
// the fractional seconds, so two timestamps can serialize to strings of
// different lengths (e.g. ".1Z" vs ".12Z"). SQLite's ORDER BY on a TEXT column
// compares lexicographically, and "the shorter string is chronologically
// earlier" does not hold once a shared prefix is followed by "Z" in one value
// and a digit in the other ('Z' > any digit), so ListRuns can report a run
// that started earlier as if it started later.
func TestListRunsOrdersChronologicallyAcrossFractionalSecondWidths(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	// earlierStart's fractional seconds (.1) format to fewer digits than
	// laterStart's (.12) despite occurring first in wall-clock time.
	earlierStart := base.Add(100 * time.Millisecond)
	laterStart := base.Add(120 * time.Millisecond)
	if !earlierStart.Before(laterStart) {
		t.Fatalf("test setup invariant violated: %v is not before %v", earlierStart, laterStart)
	}

	// Separate provider accounts so both runs can be admitted concurrently
	// without tripping the per-provider concurrency limit.
	admit := func(providerAccountID, taskID, runID string, startedAt time.Time) {
		t.Helper()
		profile := domain.ExecutionProfile{
			ID: "p-" + taskID, ProviderAccountID: providerAccountID,
			HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
		}
		if err := db.CreateProfile(ctx, profile, base); err != nil {
			t.Fatal(err)
		}
		task := domain.Task{
			ID: taskID, Name: taskID, ExecutionProfileID: profile.ID, Type: domain.OneOff,
		}
		if err := db.CreateTask(ctx, task, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(ctx, runID, taskID, providerAccountID, "", startedAt); err != nil {
			t.Fatal(err)
		}
	}
	admit("codex-earlier", "task-earlier", "run-earlier", earlierStart)
	admit("codex-later", "task-later", "run-later", laterStart)

	runs, err := db.ListRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].ID != "run-later" || runs[1].ID != "run-earlier" {
		t.Fatalf("ListRuns order = [%s, %s], want [run-later, run-earlier] (most recently started first)",
			runs[0].ID, runs[1].ID)
	}
}
