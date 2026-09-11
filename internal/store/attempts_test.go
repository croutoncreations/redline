package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func TestDispatchAttemptsRoundTripNewestFirst(t *testing.T) {
	db := openTaskDB(t)
	start := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "profile", ProviderAccountID: "codex-main", HarnessType: "codex-cli", WorkspaceProvider: "devx",
	}, start); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "requested-task", Name: "Requested", ExecutionProfileID: "profile", Type: domain.OneOff,
	}, start); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []domain.DispatchAttempt{
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchError,
			Error: "OpenUsage unavailable", StartedAt: start, CompletedAt: start.Add(time.Second)},
		{ProviderAccountID: "codex-main", Trigger: "manual", Outcome: domain.DispatchWait,
			Decision: "WAIT", Mode: "pace_threshold", Reason: "no threshold matched",
			RequestedTaskID: "requested-task",
			StartedAt:       start.Add(time.Minute), CompletedAt: start.Add(time.Minute + time.Second)},
	} {
		if _, err := db.RecordDispatchAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListDispatchAttempts(context.Background(), "codex-main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Trigger != "manual" || got[1].Error != "OpenUsage unavailable" {
		t.Fatalf("attempts = %#v", got)
	}
	if got[0].RequestedTaskID != "requested-task" {
		t.Fatalf("requested task id = %q", got[0].RequestedTaskID)
	}
}

// formatTime uses time.RFC3339Nano, whose fractional-second component is
// variable-width and omitted entirely when nanoseconds are exactly zero.
// Two attempts completing within the same whole second, one exactly on the
// second and one a fraction later, must still sort newest-first because
// ListDispatchAttempts orders by completed_at DESC as TEXT in SQLite.
func TestDispatchAttemptsOrderAcrossWholeSecondBoundary(t *testing.T) {
	db := openTaskDB(t)
	start := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for _, attempt := range []domain.DispatchAttempt{
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchWait,
			Decision: "WAIT", StartedAt: start, CompletedAt: start},
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchWait,
			Decision: "WAIT", StartedAt: start, CompletedAt: start.Add(500 * time.Millisecond)},
	} {
		if _, err := db.RecordDispatchAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListDispatchAttempts(context.Background(), "codex-main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("attempts = %#v", got)
	}
	if !got[0].CompletedAt.After(got[1].CompletedAt) {
		t.Fatalf("expected newest-first order, got %v then %v", got[0].CompletedAt, got[1].CompletedAt)
	}
}

func TestDispatchAttemptValidation(t *testing.T) {
	db := openTaskDB(t)
	_, err := db.RecordDispatchAttempt(context.Background(), domain.DispatchAttempt{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestListDispatchAttemptsRangeFiltersTriggerAndTime(t *testing.T) {
	db := openTaskDB(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, attempt := range []domain.DispatchAttempt{
		{ProviderAccountID: "claude", Trigger: "automatic", Outcome: domain.DispatchWait, Decision: "WAIT", StartedAt: start, CompletedAt: start.Add(time.Minute)},
		{ProviderAccountID: "claude", Trigger: "manual", Outcome: domain.DispatchWait, Decision: "WAIT", StartedAt: start.Add(time.Hour), CompletedAt: start.Add(time.Hour + time.Minute)},
		{ProviderAccountID: "codex", Trigger: "automatic", Outcome: domain.DispatchWait, Decision: "WAIT", StartedAt: start.Add(2 * time.Hour), CompletedAt: start.Add(2*time.Hour + time.Minute)},
	} {
		if _, err := db.RecordDispatchAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListDispatchAttemptsRange(context.Background(), "automatic", start.Add(30*time.Minute), start.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProviderAccountID != "codex" {
		t.Fatalf("attempts = %#v", got)
	}
}
