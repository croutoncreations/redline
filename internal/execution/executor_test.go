package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/execution"
	"github.com/jfox/redline/internal/harness"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/workspace"
)

func TestSuccessfulExecutionCompletesRunAndRequeuesRecurringTask(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.Recurring)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir(), SessionID: "session"}},
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 0, OutputFile: "/tmp/out", ErrorFile: "/tmp/err"}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(context.Background(), run.ID)
	storedTask, _ := db.GetTask(context.Background(), task.ID)
	if storedRun.State != domain.RunCompleted || storedRun.FinalizeState != "completed" {
		t.Fatalf("run = %#v", storedRun)
	}
	if storedTask.State != domain.Queued || storedTask.LastSuccessfulSourceRevision != "abc" {
		t.Fatalf("task = %#v", storedTask)
	}
}

func TestSuccessfulExecutionRecordsLifecycleTimelineWithoutPrompt(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	task.Prompt = "do not copy this prompt into audit logs"
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 0}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	events, err := db.ListRunEvents(context.Background(), run.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		domain.RunEventStarted,
		domain.RunEventWorkspacePrepare,
		domain.RunEventWorkspacePrepared,
		domain.RunEventHarnessStarted,
		domain.RunEventHarnessCompleted,
		domain.RunEventFinalizeStarted,
		domain.RunEventFinalizeCompleted,
		domain.RunEventCleanupStarted,
		domain.RunEventCleanupCompleted,
		domain.RunEventCompleted,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for index := range want {
		if events[index].Type != want[index] {
			t.Fatalf("event %d = %q, want %q", index, events[index].Type, want[index])
		}
		if !json.Valid(events[index].Payload) {
			t.Fatalf("event %d payload is invalid: %q", index, events[index].Payload)
		}
		if strings.Contains(string(events[index].Payload), task.Prompt) {
			t.Fatalf("event %d leaked task prompt: %s", index, events[index].Payload)
		}
	}
}

func TestExecutionEmitsStartedThenCompletedNotifications(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	notifier := &fakeNotifier{}
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness: &fakeHarness{result: harness.Result{ExitCode: 0}}, Notifier: notifier,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 2 ||
		notifier.events[0].Type != domain.EventRunStarted ||
		notifier.events[1].Type != domain.EventRunCompleted ||
		notifier.events[0].RunID != run.ID || notifier.events[0].TaskID != task.ID {
		t.Fatalf("events = %#v", notifier.events)
	}
}

func TestExecutionRecordsOwnedRunUsageWithoutChangingOutcome(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	recorder := &fakeUsageRecorder{err: errors.New("malformed usage")}
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:       &fakeHarness{result: harness.Result{ExitCode: 0, OutputFile: "/tmp/out"}},
		UsageRecorder: recorder, OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(context.Background(), run.ID)
	if stored.State != domain.RunCompleted || recorder.calls != 1 {
		t.Fatalf("run=%#v recorder=%#v", stored, recorder)
	}
	events, err := db.ListRunEvents(t.Context(), run.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.Type == domain.RunEventUsageFailed
	}
	if !found {
		t.Fatalf("usage failure missing from events: %#v", events)
	}
}

func TestNotificationFailureDoesNotChangeRunResult(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 0}},
		Notifier:        &fakeNotifier{err: errors.New("notification offline")},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(context.Background(), run.ID)
	if stored.State != domain.RunCompleted {
		t.Fatalf("run = %#v", stored)
	}
}

func TestFinalizeFailureDoesNotChangeSuccessfulAgentOutcome(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{
			workspace: domain.Workspace{Directory: t.TempDir()}, finalizeErr: fmt.Errorf("push failed"),
		},
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 0}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(context.Background(), run.ID)
	storedTask, _ := db.GetTask(context.Background(), task.ID)
	if storedRun.State != domain.RunCompleted || storedRun.FinalizeState != "failed" || storedTask.State != domain.Completed {
		t.Fatalf("run=%#v task=%#v", storedRun, storedTask)
	}
}

func TestHarnessFailureFailsTaskButStillFinalizes(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	workspaces := &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}}
	notifier := &fakeNotifier{}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces,
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 7}},
		Notifier:        notifier,
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(context.Background(), run.ID)
	storedTask, _ := db.GetTask(context.Background(), task.ID)
	if storedRun.State != domain.RunFailed || storedTask.State != domain.Failed || !workspaces.finalized {
		t.Fatalf("run=%#v task=%#v finalized=%v", storedRun, storedTask, workspaces.finalized)
	}
	if len(notifier.events) != 2 ||
		notifier.events[0].Type != domain.EventRunStarted ||
		notifier.events[1].Type != domain.EventRunFailed {
		t.Fatalf("events = %#v", notifier.events)
	}
}

func TestHarnessFailurePersistsActionableDiagnosisAndIncludesItInNotification(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	notifier := &fakeNotifier{}
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir()}},
		Harness: &fakeHarness{result: harness.Result{
			ExitCode: 1,
			Failure:  "Claude Code is signed out. Run `claude auth login`, then retry this job.",
		}},
		Notifier: notifier, OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(t.Context(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	storedRun, _ := db.GetRun(t.Context(), run.ID)
	if storedRun.Error != "Claude Code is signed out. Run `claude auth login`, then retry this job." {
		t.Fatalf("run error = %q", storedRun.Error)
	}
	if len(notifier.events) != 2 || notifier.events[1].Data["error"] != storedRun.Error {
		t.Fatalf("notification = %#v", notifier.events)
	}
}

func TestPartiallyPreparedWorkspaceIsRecordedAndCleanedOnSetupFailure(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	profile.CleanupPolicy = "always"
	workspaces := &fakeWorkspaces{
		workspace:  domain.Workspace{Directory: t.TempDir(), SessionID: "partial"},
		prepareErr: fmt.Errorf("setup failed"),
	}
	executor := execution.Executor{
		Store: db, Workspaces: workspaces, Harness: &fakeHarness{},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(context.Background(), run.ID)
	if stored.State != domain.RunFailed || stored.Workspace.SessionID != "partial" || !workspaces.cleaned {
		t.Fatalf("run=%#v cleaned=%v", stored, workspaces.cleaned)
	}
}

func TestPreparedWorkspaceIsCleanedWhenRecordingItFails(t *testing.T) {
	workspaces := &fakeWorkspaces{workspace: domain.Workspace{Directory: t.TempDir(), SessionID: "prepared"}}
	store := &failingMarkStore{}
	executor := execution.Executor{
		Store: store, Workspaces: workspaces, Harness: &fakeHarness{},
		OutputDirectory: t.TempDir(), Now: time.Now,
	}
	run := domain.Run{ID: "run", TaskID: "task", ProviderAccountID: "codex-main", StartedAt: time.Now()}
	task := domain.Task{ID: "task", Name: "Task", Type: domain.OneOff}
	profile := domain.ExecutionProfile{CleanupPolicy: "always"}

	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	if !workspaces.cleaned {
		t.Fatal("prepared workspace was not cleaned after persistence failure")
	}
	if store.completion.FinalizeState != "cleanup_completed" {
		t.Fatalf("completion = %#v", store.completion)
	}
}

func TestCleanupFailureDoesNotChangeSuccessfulAgentOutcome(t *testing.T) {
	db, run, task, profile := admittedRun(t, domain.OneOff)
	executor := execution.Executor{
		Store: db, Workspaces: &fakeWorkspaces{
			workspace: domain.Workspace{Directory: t.TempDir()}, cleanupErr: fmt.Errorf("remove failed"),
		},
		Harness:         &fakeHarness{result: harness.Result{ExitCode: 0}},
		OutputDirectory: t.TempDir(), Now: steppedClock(run.StartedAt),
	}
	if err := executor.Execute(context.Background(), run, task, profile); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetRun(context.Background(), run.ID)
	if stored.State != domain.RunCompleted || stored.FinalizeState != "failed" || !strings.Contains(stored.FinalizeError, "remove failed") {
		t.Fatalf("run = %#v", stored)
	}
}

func admittedRun(
	t *testing.T,
	taskType domain.TaskType,
) (*store.DB, domain.Run, domain.Task, domain.ExecutionProfile) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}
	task := domain.Task{
		ID: "task", Name: "Task", Prompt: "work", Priority: 50,
		ExecutionProfileID: profile.ID, Type: taskType,
	}
	if err := db.CreateProfile(context.Background(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), task, now); err != nil {
		t.Fatal(err)
	}
	run, err := db.AdmitTask(context.Background(), "run", task.ID, "codex-main", "abc", now)
	if err != nil {
		t.Fatal(err)
	}
	return db, run, task, profile
}

type fakeWorkspaces struct {
	workspace   domain.Workspace
	prepareErr  error
	finalizeErr error
	cleanupErr  error
	finalized   bool
	cleaned     bool
}

func (f *fakeWorkspaces) Prepare(context.Context, string, string, domain.ExecutionProfile) (domain.Workspace, error) {
	return f.workspace, f.prepareErr
}

func (f *fakeWorkspaces) Finalize(context.Context, workspace.FinalizeRequest) error {
	f.finalized = true
	return f.finalizeErr
}

func (f *fakeWorkspaces) Cleanup(context.Context, workspace.CleanupRequest) error {
	f.cleaned = true
	return f.cleanupErr
}

type fakeHarness struct {
	result harness.Result
	err    error
}

type failingMarkStore struct{ completion domain.RunCompletion }

func (f *failingMarkStore) MarkRunRunning(context.Context, string, domain.Workspace) error {
	return errors.New("database unavailable")
}

func (f *failingMarkStore) CompleteRun(_ context.Context, _ string, completion domain.RunCompletion, _ time.Time) error {
	f.completion = completion
	return nil
}

func (f *failingMarkStore) RecordRunEvent(context.Context, domain.RunEvent) (domain.RunEvent, error) {
	return domain.RunEvent{}, nil
}

type fakeNotifier struct {
	events []domain.NotificationEvent
	err    error
}

type fakeUsageRecorder struct {
	calls int
	err   error
}

func (r *fakeUsageRecorder) RecordRunUsage(context.Context, domain.Run, domain.ExecutionProfile, string, time.Time) (int, error) {
	r.calls++
	return 0, r.err
}

func (n *fakeNotifier) Notify(_ context.Context, event domain.NotificationEvent) error {
	n.events = append(n.events, event)
	return n.err
}

func (f *fakeHarness) Run(context.Context, harness.Request) (harness.Result, error) {
	return f.result, f.err
}

func steppedClock(start time.Time) func() time.Time {
	current := start
	return func() time.Time {
		current = current.Add(time.Minute)
		return current
	}
}
