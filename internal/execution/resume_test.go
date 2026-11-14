package execution_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/execution"
	"github.com/croutoncreations/redline/internal/harness"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/workspace"
)

var resetsAt = time.Date(2026, 7, 16, 22, 0, 0, 0, time.UTC)

func usageLimited(sessionID string) harness.Result {
	return harness.Result{
		ExitCode: 1, SessionID: sessionID,
		Failure:    "Claude Code reached the account's usage limit (five hour); it resets at 2026-07-16T22:00:00Z.",
		UsageLimit: &harness.UsageLimit{ResetsAt: resetsAt, LimitType: "five_hour"},
	}
}

func claudeRun(t *testing.T) (*store.DB, domain.Run, domain.Task, domain.ExecutionProfile) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "claude-main", HarnessType: "claude-code",
		WorkspaceProvider: "git-worktree", CleanupPolicy: "always", Repository: t.TempDir(),
	}
	task := domain.Task{ID: "task", Name: "Task", Prompt: "work", Priority: 50,
		ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	run, err := db.AdmitTask(t.Context(), "run-1", task.ID, profile.ProviderAccountID, "abc", now)
	if err != nil {
		t.Fatal(err)
	}
	return db, run, task, profile
}

// workDir makes a run workspace inside the profile's repository, as the
// git-worktree provider does.
func workDir(t *testing.T, profile domain.ExecutionProfile) string {
	t.Helper()
	dir, err := os.MkdirTemp(profile.Repository, "worktree-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type recordingHarness struct {
	results  []harness.Result
	requests []harness.Request
}

func (h *recordingHarness) Run(_ context.Context, request harness.Request) (harness.Result, error) {
	h.requests = append(h.requests, request)
	result := h.results[0]
	h.results = h.results[1:]
	return result, nil
}

type countingWorkspaces struct {
	fakeWorkspaces
	prepared   int
	finalizes  int
	cleanups   int
	lastPolicy string
}

func (c *countingWorkspaces) Prepare(ctx context.Context, runID, name string, profile domain.ExecutionProfile) (domain.Workspace, error) {
	c.prepared++
	return c.fakeWorkspaces.Prepare(ctx, runID, name, profile)
}

func (c *countingWorkspaces) Finalize(ctx context.Context, request workspace.FinalizeRequest) error {
	c.finalizes++
	return c.fakeWorkspaces.Finalize(ctx, request)
}

func (c *countingWorkspaces) Cleanup(ctx context.Context, request workspace.CleanupRequest) error {
	c.cleanups++
	return c.fakeWorkspaces.Cleanup(ctx, request)
}

func admitAgain(t *testing.T, db *store.DB, runID string, task domain.Task, at time.Time) (domain.Run, domain.Task) {
	t.Helper()
	run, err := db.AdmitTask(t.Context(), runID, task.ID, "claude-main", "abc", at)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, stored
}

func TestUsageLimitedRunQueuesTaskToContinueAfterReset(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	dir := t.TempDir()
	workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: dir, Branch: "redline/run-1"}}}
	notifier := &fakeNotifier{}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Notifier: notifier,
		Harness:         &recordingHarness{results: []harness.Result{usageLimited("sess-1")}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(t.Context(), run.ID)
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	if storedRun.State != domain.RunFailed || storedRun.Outcome != domain.RunOutcomeUsageLimited ||
		storedRun.External.SessionID != "sess-1" || storedRun.FinalizeState != domain.FinalizeDeferred ||
		!strings.Contains(storedRun.Summary, "continue this session") {
		t.Fatalf("run = %#v", storedRun)
	}
	wantAfter := resetsAt.Add(time.Minute)
	if storedTask.State != domain.Queued || storedTask.ResumeAfter == nil || !storedTask.ResumeAfter.Equal(wantAfter) ||
		storedTask.ResumeRunID != run.ID || storedTask.ResumeAttempts != 1 {
		t.Fatalf("task = %#v", storedTask)
	}
	// The partial work stays for the next run: no finalize hook, no cleanup.
	if workspaces.finalizes != 0 || workspaces.cleanups != 0 {
		t.Fatalf("finalize=%d cleanup=%d", workspaces.finalizes, workspaces.cleanups)
	}
	paused, _ := db.ProviderPaused(t.Context(), run.ProviderAccountID)
	if paused {
		t.Fatal("a usage limit must not pause the provider")
	}
	last := notifier.events[len(notifier.events)-1]
	if last.Data["outcome"] != domain.RunOutcomeUsageLimited || last.Data["resume_after"] != wantAfter.Format(time.RFC3339) {
		t.Fatalf("notification = %#v", last)
	}
}

func TestResumedRunContinuesSessionInSameWorkspaceThenFinishesTask(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	dir := workDir(t, profile)
	workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: dir, Branch: "redline/run-1"}}}
	agent := &recordingHarness{results: []harness.Result{usageLimited("sess-1"), {ExitCode: 0, SessionID: "sess-1"}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: agent,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	if workspaces.prepared != 1 {
		t.Fatalf("workspace prepared %d times, want once", workspaces.prepared)
	}
	resumed := agent.requests[1]
	if resumed.ResumeSessionID != "sess-1" || resumed.Workspace.Directory != dir || resumed.Workspace.Branch != "redline/run-1" {
		t.Fatalf("resumed request = %#v", resumed)
	}
	if workspaces.finalizes != 1 || workspaces.cleanups != 1 {
		t.Fatalf("finalize=%d cleanup=%d", workspaces.finalizes, workspaces.cleanups)
	}
	storedRun, _ := db.GetRun(t.Context(), second.ID)
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	if storedRun.State != domain.RunCompleted || storedRun.Workspace.Directory != dir {
		t.Fatalf("run = %#v", storedRun)
	}
	if first, _ := db.GetRun(t.Context(), run.ID); first.FinalizeState != domain.FinalizeContinued {
		t.Fatalf("kept run = %#v", first)
	}
	if storedTask.State != domain.Completed || storedTask.ResumeAfter != nil || storedTask.ResumeRunID != "" || storedTask.ResumeAttempts != 0 {
		t.Fatalf("task = %#v", storedTask)
	}
}

func TestResumeStartsAfreshWhenEarlierWorkspaceIsGone(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	gone := filepath.Join(t.TempDir(), "removed")
	fresh := t.TempDir()
	workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: gone}}}
	agent := &recordingHarness{results: []harness.Result{usageLimited("sess-1"), {ExitCode: 0}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: agent,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	workspaces.workspace = domain.Workspace{Directory: fresh}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	if workspaces.prepared != 2 || agent.requests[1].ResumeSessionID != "" || agent.requests[1].Workspace.Directory != fresh {
		t.Fatalf("prepared=%d request=%#v", workspaces.prepared, agent.requests[1])
	}
	events, _ := db.ListRunEvents(t.Context(), second.ID, 100)
	found := false
	for _, event := range events {
		found = found || (event.Type == domain.RunEventResumeFallback && strings.Contains(string(event.Payload), "no longer exists"))
	}
	if !found {
		t.Fatalf("fresh-start reason missing from events: %#v", events)
	}
}

type keyedWorkspaces struct {
	countingWorkspaces
	finalized []string
	cleaned   []string
}

func (k *keyedWorkspaces) Finalize(ctx context.Context, request workspace.FinalizeRequest) error {
	k.finalized = append(k.finalized, request.RunID+":"+request.Workspace.Directory)
	return k.countingWorkspaces.Finalize(ctx, request)
}

func (k *keyedWorkspaces) Cleanup(ctx context.Context, request workspace.CleanupRequest) error {
	k.cleaned = append(k.cleaned, request.Workspace.Directory)
	return k.countingWorkspaces.Cleanup(ctx, request)
}

func TestFreshStartAfterEditFinalizesAndCleansKeptWorkspace(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	keptDir, freshDir := workDir(t, profile), workDir(t, profile)
	workspaces := &keyedWorkspaces{countingWorkspaces: countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: keptDir}}}}
	agent := &recordingHarness{results: []harness.Result{usageLimited("sess-1"), {ExitCode: 0}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: agent,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	task.Prompt = "different work"
	if err := db.UpdateTask(t.Context(), task, resetsAt); err != nil {
		t.Fatal(err)
	}
	workspaces.workspace = domain.Workspace{Directory: freshDir}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	if agent.requests[1].ResumeSessionID != "" || agent.requests[1].Workspace.Directory != freshDir {
		t.Fatalf("second request = %#v", agent.requests[1])
	}
	wantFinalized := []string{run.ID + ":" + keptDir, second.ID + ":" + freshDir}
	if strings.Join(workspaces.finalized, ",") != strings.Join(wantFinalized, ",") ||
		strings.Join(workspaces.cleaned, ",") != keptDir+","+freshDir {
		t.Fatalf("finalized=%v cleaned=%v", workspaces.finalized, workspaces.cleaned)
	}
	if first, _ := db.GetRun(t.Context(), run.ID); first.FinalizeState != domain.FinalizeContinued {
		t.Fatalf("kept run = %#v", first)
	}
}

func TestChangedProfileRepositoryStartsFreshAndCleansKeptWorkspace(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	keptDir := workDir(t, profile)
	workspaces := &keyedWorkspaces{countingWorkspaces: countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: keptDir}}}}
	agent := &recordingHarness{results: []harness.Result{usageLimited("sess-1"), {ExitCode: 0}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: agent,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	profile.Repository = t.TempDir()
	workspaces.workspace = domain.Workspace{Directory: workDir(t, profile)}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	if agent.requests[1].ResumeSessionID != "" || len(workspaces.cleaned) != 2 || workspaces.cleaned[0] != keptDir {
		t.Fatalf("request=%#v cleaned=%v", agent.requests[1], workspaces.cleaned)
	}
}

func TestSharedDirectoryProfilesNeverDeferFinalize(t *testing.T) {
	for _, provider := range []string{"existing-directory", "runtime-owned", "command"} {
		t.Run(provider, func(t *testing.T) {
			db, run, task, profile := claudeRun(t)
			profile.WorkspaceProvider = provider
			workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}}
			executor := execution.Executor{
				Store: db, Workspaces: workspaces,
				Harness:         &recordingHarness{results: []harness.Result{usageLimited("sess-1")}},
				OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
			}
			if err := executor.Execute(t.Context(), run, task, profile); err != nil {
				t.Fatal(err)
			}
			stored, _ := db.GetTask(t.Context(), task.ID)
			if stored.State != domain.Queued || stored.ResumeRunID != "" || workspaces.finalizes != 1 || workspaces.cleanups != 1 {
				t.Fatalf("task=%#v finalize=%d cleanup=%d", stored, workspaces.finalizes, workspaces.cleanups)
			}
		})
	}
}

func TestRejectedResumeRequeuesFreshInsteadOfFailing(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	dir := workDir(t, profile)
	workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: dir}}}
	agent := &recordingHarness{results: []harness.Result{
		usageLimited("sess-1"),
		{ExitCode: 1, ResumeRejected: true, Failure: ""},
	}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: agent,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetTask(t.Context(), task.ID)
	if stored.State != domain.Queued || stored.ResumeRunID != "" || stored.ResumeAfter == nil {
		t.Fatalf("task = %#v", stored)
	}
	// The shared workspace finished its lifecycle on the rejected run.
	if workspaces.finalizes != 1 || workspaces.cleanups != 1 {
		t.Fatalf("finalize=%d cleanup=%d", workspaces.finalizes, workspaces.cleanups)
	}
}

func TestUsageLimitWithoutSessionRequeuesFreshAndCleansUp(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	profile.HarnessArgs = []string{"--no-session-persistence"}
	workspaces := &countingWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces,
		Harness:         &recordingHarness{results: []harness.Result{usageLimited("sess-1")}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	storedRun, _ := db.GetRun(t.Context(), run.ID)
	if storedTask.State != domain.Queued || storedTask.ResumeRunID != "" || storedTask.ResumeAfter == nil {
		t.Fatalf("task = %#v", storedTask)
	}
	if storedRun.Outcome != domain.RunOutcomeUsageLimited || !strings.Contains(storedRun.Summary, "start it again") {
		t.Fatalf("run = %#v", storedRun)
	}
	// Nothing will continue this workspace, so its normal lifecycle runs.
	if workspaces.finalizes != 1 || workspaces.cleanups != 1 {
		t.Fatalf("finalize=%d cleanup=%d", workspaces.finalizes, workspaces.cleanups)
	}
}

func TestRepeatedUsageLimitsEventuallyFailTask(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	results := make([]harness.Result, 0, execution.MaxUsageLimitResumes+1)
	for range execution.MaxUsageLimitResumes + 1 {
		results = append(results, usageLimited("sess-1"))
	}
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:         &recordingHarness{results: results},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	at := resetsAt
	for attempt := 0; ; attempt++ {
		if err := executor.Execute(t.Context(), run, task, profile); err != nil {
			t.Fatal(err)
		}
		stored, _ := db.GetTask(t.Context(), task.ID)
		if attempt == execution.MaxUsageLimitResumes {
			if stored.State != domain.Failed || stored.ResumeAttempts != 0 {
				t.Fatalf("task after %d limits = %#v", attempt+1, stored)
			}
			return
		}
		if stored.State != domain.Queued || stored.ResumeAttempts != attempt+1 {
			t.Fatalf("task after %d limits = %#v", attempt+1, stored)
		}
		at = at.Add(time.Hour)
		run, task = admitAgain(t, db, "run-"+string(rune('a'+attempt)), task, at)
	}
}

func TestUsageLimitWithoutResetTimeWaitsFallbackInterval(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	result := usageLimited("sess-1")
	result.UsageLimit.ResetsAt = time.Time{}
	clock := steppedClock(run.StartedAt)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:         &recordingHarness{results: []harness.Result{result}},
		OutputDirectory: t.TempDir(), Now: clock,
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetTask(t.Context(), task.ID)
	if stored.ResumeAfter == nil || !stored.ResumeAfter.After(run.StartedAt.Add(29*time.Minute)) ||
		stored.ResumeAfter.After(run.StartedAt.Add(time.Hour)) {
		t.Fatalf("resume after = %v", stored.ResumeAfter)
	}
}

func TestOrdinaryFailureAfterUsageLimitFailsTaskAndClearsResume(t *testing.T) {
	db, run, task, profile := claudeRun(t)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:         &recordingHarness{results: []harness.Result{usageLimited("sess-1"), {ExitCode: 1, Failure: "tests failed"}}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	second, task := admitAgain(t, db, "run-2", task, resetsAt.Add(2*time.Minute))
	if err := executor.Execute(t.Context(), second, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetTask(t.Context(), task.ID)
	if stored.State != domain.Failed || stored.ResumeRunID != "" || stored.ResumeAfter != nil {
		t.Fatalf("task = %#v", stored)
	}
}
