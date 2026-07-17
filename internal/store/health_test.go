package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func TestOperationalHealthSummarizesRecentFailuresAndActiveRuns(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if _, err := db.RecordDispatchAttempt(context.Background(), domain.DispatchAttempt{
		ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchError,
		Error: "offline", StartedAt: now.Add(-time.Minute), CompletedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	deliveryID, err := db.CreateNotificationDelivery(context.Background(), domain.EventSchedulerError, json.RawMessage(`{}`), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotificationDelivery(context.Background(), deliveryID, "failed", "hook failed", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProfile(context.Background(), domain.ExecutionProfile{
		ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory",
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), domain.Task{ID: "t", Name: "Task", ExecutionProfileID: "p", Type: domain.OneOff}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(context.Background(), "r", "t", "codex-main", "", now); err != nil {
		t.Fatal(err)
	}
	health, err := db.OperationalHealth(context.Background(), now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "degraded" || health.ActiveRuns != 1 || health.DispatchAttempts != 1 ||
		health.DispatchErrors != 1 || health.NotificationFailures != 1 {
		t.Fatalf("health = %#v", health)
	}
}

func TestOperationalHealthRecoversAfterFailuresAgeOut(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-25 * time.Hour)
	if _, err := db.RecordDispatchAttempt(context.Background(), domain.DispatchAttempt{
		ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchError,
		Error: "offline", StartedAt: old, CompletedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	deliveryID, err := db.CreateNotificationDelivery(context.Background(), domain.EventSchedulerError, json.RawMessage(`{}`), old)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotificationDelivery(context.Background(), deliveryID, "failed", "hook failed", old); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateProfile(context.Background(), domain.ExecutionProfile{
		ID: "p", ProviderAccountID: "codex-main", HarnessType: "command", WorkspaceProvider: "existing-directory",
	}, old); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(context.Background(), domain.Task{
		ID: "t", Name: "Task", ExecutionProfileID: "p", Type: domain.OneOff,
	}, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTask(context.Background(), "r", "t", "codex-main", "", old); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(context.Background(), "r", domain.RunCompletion{
		State: domain.RunFailed, ExitCode: 1, Error: "failed", FinalizeState: "completed",
	}, old); err != nil {
		t.Fatal(err)
	}

	health, err := db.OperationalHealth(context.Background(), now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "healthy" || health.FailedRuns != 0 || health.DispatchErrors != 0 || health.NotificationFailures != 0 {
		t.Fatalf("health = %#v", health)
	}
}
