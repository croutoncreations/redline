package execution_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/execution"
	"github.com/croutoncreations/redline/internal/harness"
	"github.com/croutoncreations/redline/internal/workspace"
)

// hookWorkspaces records cleanup policy and usage-limit hook calls.
type hookWorkspaces struct {
	fakeWorkspaces
	hookErr       error
	hookRequest   *workspace.UsageLimitRequest
	cleanupPolicy string
	finalizeOut   string
}

func (h *hookWorkspaces) RunUsageLimitHook(_ context.Context, request workspace.UsageLimitRequest) error {
	h.hookRequest = &request
	return h.hookErr
}

func (h *hookWorkspaces) Cleanup(_ context.Context, request workspace.CleanupRequest) error {
	h.cleanupPolicy = request.Profile.CleanupPolicy
	h.cleaned = true
	return nil
}

func (h *hookWorkspaces) Finalize(_ context.Context, request workspace.FinalizeRequest) error {
	h.finalized = true
	h.finalizeOut = request.Outcome
	return nil
}

func limitedResult() harness.Result {
	resets := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	return harness.Result{
		ExitCode: 1, Failure: "Claude Code stopped at its usage limit.",
		UsageLimit: &harness.UsageLimit{SessionID: "sess-1", LimitType: "five_hour", ResetsAt: &resets},
	}
}

func runTypes(t *testing.T, events []domain.RunEvent) map[string]bool {
	t.Helper()
	types := map[string]bool{}
	for _, event := range events {
		types[event.Type] = true
	}
	return types
}

func TestUsageLimitWithoutHookFailsRunAndKeepsWorkspace(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.CleanupPolicy = "always"
	workspaces := &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: &fakeHarness{result: limitedResult()},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(t.Context(), run.ID)
	if stored.State != domain.RunFailed || stored.Outcome != "usage_limited" {
		t.Fatalf("run = %#v", stored)
	}
	if workspaces.hookRequest != nil || workspaces.cleanupPolicy != "never" || workspaces.finalizeOut != "usage_limited" {
		t.Fatalf("hook=%v cleanup=%q finalize outcome=%q", workspaces.hookRequest, workspaces.cleanupPolicy, workspaces.finalizeOut)
	}
	if paused, _ := db.ProviderPaused(t.Context(), run.ProviderAccountID); paused {
		t.Fatal("a usage limit must not pause the provider like a sign-out")
	}
	events, _ := db.ListRunEvents(t.Context(), run.ID, 100)
	if !runTypes(t, events)[domain.RunEventUsageLimitReached] {
		t.Fatalf("events = %#v", events)
	}
}

func TestUsageLimitHookThatSucceedsCompletesTheRun(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.OnUsageLimitCommand = "./take-over.sh"
	profile.CleanupPolicy = "on_success"
	workspaces := &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}}
	notifier := &fakeNotifier{}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: &fakeHarness{result: limitedResult()}, Notifier: notifier,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	request := workspaces.hookRequest
	if request == nil || request.Prompt != "work" || request.HarnessSessionID != "sess-1" ||
		request.LimitType != "five_hour" || request.ResetsAt != "2026-11-14T15:00:00Z" {
		t.Fatalf("hook request = %#v", request)
	}
	stored, _ := db.GetRun(t.Context(), run.ID)
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	if stored.State != domain.RunCompleted || stored.Outcome != "handed_off" || stored.Error != "" ||
		storedTask.State != domain.Completed {
		t.Fatalf("run=%#v task=%#v", stored, storedTask)
	}
	if workspaces.cleanupPolicy != "on_success" || workspaces.finalizeOut != "handed_off" {
		t.Fatalf("cleanup=%q finalize=%q", workspaces.cleanupPolicy, workspaces.finalizeOut)
	}
	last := notifier.events[len(notifier.events)-1]
	if last.Type != domain.EventRunCompleted || last.Data["outcome"] != "handed_off" {
		t.Fatalf("notification = %#v", last)
	}
	events, _ := db.ListRunEvents(t.Context(), run.ID, 100)
	if types := runTypes(t, events); !types[domain.RunEventUsageLimitHook] || !types[domain.RunEventUsageLimitHanded] {
		t.Fatalf("events = %#v", events)
	}
}

func TestUsageLimitHookThatFailsLeavesRunFailedWithWorkspaceKept(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.OnUsageLimitCommand = "./take-over.sh"
	profile.CleanupPolicy = "always"
	workspaces := &hookWorkspaces{
		fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		hookErr:        errors.New("usage limit hook exited with code 3"),
	}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: &fakeHarness{result: limitedResult()},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(t.Context(), run.ID)
	if stored.State != domain.RunFailed || stored.Outcome != "usage_limited" || workspaces.cleanupPolicy != "never" {
		t.Fatalf("run=%#v cleanup=%q", stored, workspaces.cleanupPolicy)
	}
	events, _ := db.ListRunEvents(t.Context(), run.ID, 100)
	if !runTypes(t, events)[domain.RunEventUsageLimitFailed] {
		t.Fatalf("events = %#v", events)
	}
}

func TestUsageLimitRetryRequeuesTaskUntilTheLimitResets(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.RetryAfterUsageLimit = true
	notifier := &fakeNotifier{}
	workspaces := &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: &fakeHarness{result: limitedResult()}, Notifier: notifier,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(t.Context(), run.ID)
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	want := time.Date(2026, 11, 14, 15, 2, 0, 0, time.UTC)
	if stored.State != domain.RunFailed || storedTask.State != domain.Queued ||
		storedTask.RetryNotBefore == nil || !storedTask.RetryNotBefore.Equal(want) || storedTask.UsageLimitRetries != 1 {
		t.Fatalf("run=%#v task=%#v", stored, storedTask)
	}
	if last := notifier.events[len(notifier.events)-1]; last.Data["retry_at"] != "2026-11-14T15:02:00Z" {
		t.Fatalf("notification = %#v", last)
	}
	// Held: admission refuses the task until the reset.
	if _, err := db.AdmitTask(t.Context(), "early", task.ID, run.ProviderAccountID, "abc", want.Add(-time.Minute)); err == nil {
		t.Fatal("a held task was admitted before its limit reset")
	}
	if _, err := db.AdmitTask(t.Context(), "after", task.ID, run.ProviderAccountID, "abc", want.Add(time.Minute)); err != nil {
		t.Fatalf("held task was not admitted after its reset: %v", err)
	}
}

func TestUsageLimitRetryUsesFallbackWithoutResetAndStopsAtTheCap(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.RetryAfterUsageLimit = true
	task.UsageLimitRetries = domain.MaxUsageLimitRetries
	result := limitedResult()
	result.UsageLimit.ResetsAt = nil
	executor := execution.Executor{
		Store: db, Workspaces: &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}},
		Harness: &fakeHarness{result: result}, OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	if storedTask.State != domain.Failed || storedTask.RetryNotBefore != nil {
		t.Fatalf("capped task = %#v", storedTask)
	}

	db, run, task, profile = admittedRun(t, domain.OneOff)
	profile.RetryAfterUsageLimit = true
	executor.Store = db
	executor.Now = steppedClock(run.StartedAt)
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedTask, _ = db.GetTask(t.Context(), task.ID)
	if storedTask.State != domain.Queued || storedTask.RetryNotBefore == nil ||
		storedTask.RetryNotBefore.Sub(run.StartedAt) < 50*time.Minute {
		t.Fatalf("fallback task = %#v", storedTask)
	}
}

func TestUsageLimitHandOffDoesNotAlsoRetry(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.Recurring)
	profile.RetryAfterUsageLimit = true
	profile.OnUsageLimitCommand = "./take-over.sh"
	executor := execution.Executor{
		Store: db, Workspaces: &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}},
		Harness: &fakeHarness{result: limitedResult()}, OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedTask, _ := db.GetTask(t.Context(), task.ID)
	if storedTask.State != domain.Queued || storedTask.RetryNotBefore != nil || storedTask.UsageLimitRetries != 0 {
		t.Fatalf("task = %#v", storedTask)
	}
}

// Drives the cap through the store: three retries are scheduled, the next
// stop marks the task failed. A recurring task takes the same path.
func TestUsageLimitRetryCapIsCountedInTheStore(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.Recurring)
	profile.RetryAfterUsageLimit = true
	clock := steppedClock(run.StartedAt)
	executor := execution.Executor{
		Store: db, Workspaces: &hookWorkspaces{fakeWorkspaces: fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}},
		Harness: &fakeHarness{result: limitedResult()}, OutputDirectory: t.TempDir(), Now: clock,
	}
	for attempt := 1; ; attempt++ {
		if err := executor.Execute(t.Context(), run, task, profile); err != nil {
			t.Fatal(err)
		}
		stored, _ := db.GetTask(t.Context(), task.ID)
		if attempt > domain.MaxUsageLimitRetries {
			if stored.State != domain.Failed || stored.RetryNotBefore != nil {
				t.Fatalf("attempt %d: task = %#v", attempt, stored)
			}
			return
		}
		if stored.State != domain.Queued || stored.UsageLimitRetries != attempt {
			t.Fatalf("attempt %d: task = %#v", attempt, stored)
		}
		admitAt := stored.RetryNotBefore.Add(time.Minute)
		next, err := db.AdmitTask(t.Context(), fmt.Sprintf("run-%d", attempt), task.ID, run.ProviderAccountID, "abc", admitAt)
		if err != nil {
			t.Fatalf("attempt %d: admit after reset: %v", attempt, err)
		}
		run, task = next, stored
	}
}
