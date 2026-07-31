package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func TestRunEventsRoundTripInTimelineOrder(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{
		ID: "profile-events", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	task := domain.Task{
		ID: "task-events", Name: "Audit me", Prompt: "secret prompt",
		Priority: 50, ExecutionProfileID: profile.ID, Type: domain.OneOff,
	}
	if err := db.CreateProfile(context.Background(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), task, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(context.Background(), "run-events", task.ID, "codex-main", "abc", now); err != nil {
		t.Fatal(err)
	}

	for index, event := range []domain.RunEvent{
		{RunID: "run-events", Type: domain.RunEventStarted, Payload: json.RawMessage(`{"task_id":"task-events"}`)},
		{RunID: "run-events", Type: domain.RunEventWorkspacePrepared, Payload: json.RawMessage(`{"directory":"/tmp/work"}`)},
	} {
		event.OccurredAt = now.Add(time.Duration(index) * time.Second)
		if _, err := db.RecordRunEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListRunEvents(context.Background(), "run-events", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != domain.RunEventStarted || got[1].Type != domain.RunEventWorkspacePrepared {
		t.Fatalf("events = %#v", got)
	}
	if got[0].ID == 0 || got[0].RunID != "run-events" || string(got[0].Payload) != `{"task_id":"task-events"}` {
		t.Fatalf("first event = %#v", got[0])
	}
}

func TestRunEventValidation(t *testing.T) {
	db := openTaskDB(t)
	if _, err := db.RecordRunEvent(context.Background(), domain.RunEvent{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestListRunEventsReturnsMostRecentWhenTruncated(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{
		ID: "profile-events-truncated", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory",
	}
	task := domain.Task{
		ID: "task-events-truncated", Name: "Audit me", Prompt: "secret prompt",
		Priority: 50, ExecutionProfileID: profile.ID, Type: domain.OneOff,
	}
	if err := db.CreateProfile(context.Background(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), task, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(context.Background(), "run-events-truncated", task.ID, "codex-main", "abc", now); err != nil {
		t.Fatal(err)
	}

	types := []string{
		domain.RunEventStarted, domain.RunEventWorkspacePrepared, domain.RunEventCompleted,
	}
	for index, eventType := range types {
		event := domain.RunEvent{
			RunID: "run-events-truncated", Type: eventType, Payload: json.RawMessage(`{}`),
			OccurredAt: now.Add(time.Duration(index) * time.Second),
		}
		if _, err := db.RecordRunEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.ListRunEvents(context.Background(), "run-events-truncated", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != domain.RunEventWorkspacePrepared || got[1].Type != domain.RunEventCompleted {
		t.Fatalf("expected the two most recent events in chronological order, got %#v", got)
	}
}
