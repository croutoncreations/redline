package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

// TestUnreadRunActivityCountAndMarkAllRead exercises MarkAllRunActivityRead and
// UnreadRunActivityCount, which were previously at 0% coverage.
//
// The bug class these guard against: a WHERE-clause error in the bulk-mark
// could silently skip some runs, leaving the unread count positive after the
// call.  A second correctness risk is that active runs (preparing/running)
// must never appear in the unread count, so the test verifies the state
// machine boundary explicitly.
// ListRuns orders by started_at DESC over a TEXT column. Under RFC3339Nano,
// 100ms encodes to ".1Z" and 120ms to ".12Z"; ".1Z" sorts above ".12Z"
// byte-wise because 'Z' > '2', so the older run would be listed first.
func TestListRunsOrdersChronologicallyAcrossFractionalSecondWidths(t *testing.T) {
	db := openTaskDB(t)
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	earlier := base.Add(100 * time.Millisecond)
	later := base.Add(120 * time.Millisecond)
	if !earlier.Before(later) {
		t.Fatalf("fixture invariant broken: %v is not before %v", earlier, later)
	}

	// Separate provider accounts so both runs can be admitted concurrently
	// without tripping the per-provider concurrency limit of 1.
	admit := func(providerAccountID, taskID, runID string, startedAt time.Time) {
		t.Helper()
		profile := domain.ExecutionProfile{
			ID: "p-" + taskID, ProviderAccountID: providerAccountID,
			HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
		}
		if err := db.CreateProfile(t.Context(), profile, base); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateTask(t.Context(), domain.Task{
			ID: taskID, Name: taskID, ExecutionProfileID: profile.ID, Type: domain.OneOff,
		}, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(t.Context(), runID, taskID, providerAccountID, "", startedAt); err != nil {
			t.Fatal(err)
		}
	}
	admit("codex-earlier", "task-earlier", "run-earlier", earlier)
	admit("codex-later", "task-later", "run-later", later)

	runs, err := db.ListRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].ID != "run-later" || runs[1].ID != "run-earlier" {
		t.Fatalf("ListRuns order = [%s, %s], want [run-later, run-earlier]", runs[0].ID, runs[1].ID)
	}
}

func TestCompletedRunCursorDoesNotLoseOlderOrBurstingRuns(t *testing.T) {
	db := openTaskDB(t)
	ctx := t.Context()
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "watch-profile", ProviderAccountID: "watch-provider",
		HarnessType: "claude-code", WorkspaceProvider: "existing-directory"}
	if err := db.CreateProfile(ctx, profile, base); err != nil {
		t.Fatal(err)
	}
	// Start a run before the watcher baseline; it completes only after the
	// baseline, when 105 newer runs have already completed.
	makeRun := func(id string, at time.Time) {
		t.Helper()
		if err := db.CreateTask(ctx, domain.Task{ID: id, Name: id, ExecutionProfileID: profile.ID, Type: domain.OneOff}, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(ctx, "run-"+id, id, profile.ProviderAccountID, "", at); err != nil {
			t.Fatal(err)
		}
	}
	makeRun("z", base)
	if err := db.CompleteRun(ctx, "run-z", domain.RunCompletion{State: domain.RunCompleted}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	baseline, err := db.LatestCompletedRunCursor(ctx)
	if err != nil || baseline != 1 {
		t.Fatalf("baseline = %d err=%v", baseline, err)
	}
	// This later commit has an earlier timestamp and lower lexical ID than
	// the baseline. A timestamp/ID cursor would silently skip it.
	makeRun("a", base)
	if err := db.CompleteRun(ctx, "run-a", domain.RunCompletion{State: domain.RunCompleted}, base.Add(time.Minute-time.Second)); err != nil {
		t.Fatal(err)
	}
	for i := range 105 {
		id := fmt.Sprintf("task-%03d", i)
		makeRun(id, base.Add(time.Duration(i+1)*time.Second))
		if err := db.CompleteRun(ctx, "run-"+id, domain.RunCompletion{State: domain.RunCompleted}, base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	makeRun("old", base)
	if err := db.CompleteRun(ctx, "run-old", domain.RunCompletion{State: domain.RunFailed}, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var ids []string
	cursor := baseline
	for {
		page, next, err := db.ListCompletedRuns(ctx, cursor, 30)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, run := range page {
			ids = append(ids, run.ID)
		}
		cursor = next
	}
	if len(ids) != 107 || ids[0] != "run-a" || ids[len(ids)-1] != "run-old" {
		t.Fatalf("cursor returned %d runs, last=%v", len(ids), ids[len(ids)-1:])
	}
}

func TestCompletionSequenceMigrationBackfillsV26AndContinuesAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	makeRun := func(id string, complete bool) {
		t.Helper()
		profile := domain.ExecutionProfile{ID: "p-" + id, ProviderAccountID: "provider-" + id,
			HarnessType: "claude-code", WorkspaceProvider: "existing-directory"}
		if err := db.CreateProfile(ctx, profile, base); err != nil {
			t.Fatal(err)
		}
		if err := db.CreateTask(ctx, domain.Task{ID: id, Name: id, ExecutionProfileID: profile.ID, Type: domain.OneOff}, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(ctx, "run-"+id, id, profile.ProviderAccountID, "", base); err != nil {
			t.Fatal(err)
		}
		if complete {
			if err := db.CompleteRun(ctx, "run-"+id, domain.RunCompletion{State: domain.RunCompleted}, base); err != nil {
				t.Fatal(err)
			}
		}
	}
	makeRun("old", true)
	makeRun("b", true)
	makeRun("a", true)
	makeRun("failed", false)
	if err := db.CompleteRun(ctx, "run-failed", domain.RunCompletion{State: domain.RunFailed}, base); err != nil {
		t.Fatal(err)
	}
	makeRun("live", false)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate the on-disk v26 layout, not merely its migration version.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX idx_runs_completion_sequence`,
		`ALTER TABLE runs DROP COLUMN completion_sequence`,
		`DELETE FROM schema_migrations WHERE version >= 27`,
		`UPDATE runs SET completed_at = '2026-09-24T11:00:00.000000000Z' WHERE id = 'run-old'`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("downgrade statement %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v26 with existing runs: %v", err)
	}
	page, cursor, err := db.ListCompletedRuns(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 4 || len(page) != 4 || page[0].ID != "run-old" || page[1].ID != "run-a" ||
		page[2].ID != "run-b" || page[3].ID != "run-failed" || page[3].State != domain.RunFailed {
		t.Fatalf("backfill order/cursor = %v / %d", page, cursor)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer db.Close()
	makeRun("new", true)
	if err := db.RecoverInterruptedRuns(ctx, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, cursor, err = db.ListCompletedRuns(ctx, 4, 100)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 6 || len(page) != 2 || page[0].ID != "run-new" ||
		page[1].ID != "run-live" || page[1].State != domain.RunFailed {
		t.Fatalf("post-migration and recovery order/cursor = %v / %d", page, cursor)
	}
	if err := db.RecoverInterruptedRuns(ctx, base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if afterRecovery, err := db.LatestCompletedRunCursor(ctx); err != nil || afterRecovery != cursor {
		t.Fatalf("repeated recovery cursor = %d, err=%v, want %d", afterRecovery, err, cursor)
	}
	// The unique index covers backfilled rows and future completions.
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var uniqueIndex string
	if err := check.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_runs_completion_sequence'`).Scan(&uniqueIndex); err != nil {
		t.Fatalf("completion index missing: %v", err)
	}
	if _, err := check.ExecContext(ctx, `UPDATE runs SET completion_sequence = 1 WHERE id = 'run-live'`); err == nil {
		t.Fatal("completion sequence index should reject duplicate values")
	}
}

func TestUnreadRunActivityCountAndMarkAllRead(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
