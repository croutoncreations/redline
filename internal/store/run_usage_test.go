package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/capacity"
	"github.com/croutoncreations/redline/internal/domain"
)

func TestRunUsageEvidenceSeeksTheUniqueIndexPerRun(t *testing.T) {
	t.Parallel()
	db := openTaskDB(t)
	plan, err := db.ExplainRunUsageObservations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "sqlite_autoindex_token_observations_1 (provider=? AND source=? AND source_id>? AND source_id<?)") ||
		strings.Contains(joined, "SCAN o") || strings.Contains(joined, "SCAN token_observations") {
		t.Fatalf("evidence query must seek the unique index per run, plan:\n%s", joined)
	}
}

func TestListRunUsageScopesRunsEvidenceAndIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTaskDB(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, profile := range []domain.ExecutionProfile{
		{ID: "original", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "existing-directory"},
		{ID: "current", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "existing-directory"},
	} {
		if err := db.CreateProfile(ctx, profile, now); err != nil {
			t.Fatal(err)
		}
	}
	admit := func(runID string, completedAt time.Time, state domain.RunState, snapshotProfile string) {
		t.Helper()
		task := domain.Task{ID: "task-" + runID, Name: runID, Prompt: "p", Priority: 50,
			ExecutionProfileID: "current", Type: domain.OneOff}
		if err := db.CreateTask(ctx, task, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.AdmitTask(ctx, runID, task.ID, "claude-main", "rev", completedAt.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if snapshotProfile != "" {
			payload, _ := json.Marshal(map[string]any{"profile": map[string]any{"id": snapshotProfile, "harness_type": "claude-code"}})
			if _, err := db.RecordRunEvent(ctx, domain.RunEvent{RunID: runID, Type: domain.RunEventStarted,
				OccurredAt: completedAt.Add(-time.Minute), Payload: payload}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.CompleteRun(ctx, runID, domain.RunCompletion{State: state}, completedAt); err != nil {
			t.Fatal(err)
		}
	}
	since, until := now, now.Add(24*time.Hour)
	admit("run-a", now.Add(time.Hour), domain.RunCompleted, "original")
	admit("run-a2", now.Add(2*time.Hour), domain.RunFailed, "")
	admit("run-before", now.Add(-time.Hour), domain.RunCompleted, "original")
	admit("run-at-end", until, domain.RunCompleted, "original")
	if _, err := db.RecordDispatchAttempt(ctx, domain.DispatchAttempt{ProviderAccountID: "claude-main",
		Trigger: "manual", Outcome: domain.DispatchAdmitted, RunID: "run-a",
		StartedAt: now, CompletedAt: now}); err != nil {
		t.Fatal(err)
	}
	at := now.Add(time.Hour)
	if _, err := db.SaveTokenObservations(ctx, []capacity.TokenObservation{
		{Provider: "claude", Source: "redline-run", SourceID: "run-a:sonnet", ObservedAt: at, InputTokens: 1},
		{Provider: "claude", Source: "redline-run", SourceID: "run-a:haiku", ObservedAt: at, OutputTokens: 2},
		// run-a2 shares run-a's prefix; it must not leak into run-a.
		{Provider: "claude", Source: "redline-run", SourceID: "run-a2", ObservedAt: at, InputTokens: 100},
		// Interactive usage is never Redline-managed.
		{Provider: "claude", Source: "gatepost", SourceID: "run-a:sonnet", ObservedAt: at, InputTokens: 1000},
		{Provider: "claude", Source: "redline-run", SourceID: "run-before:sonnet", ObservedAt: at, InputTokens: 7},
	}); err != nil {
		t.Fatal(err)
	}
	records, err := db.ListRunUsage(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].RunID != "run-a" || records[1].RunID != "run-a2" {
		t.Fatalf("records = %#v", records)
	}
	first, second := records[0], records[1]
	if first.ProfileID != "original" || first.ProfileInferred || first.Trigger != "manual" || len(first.Observations) != 2 {
		t.Fatalf("run-a = %#v", first)
	}
	if second.ProfileID != "current" || !second.ProfileInferred || second.Trigger != "" ||
		second.State != domain.RunFailed || len(second.Observations) != 1 || second.Observations[0].InputTokens != 100 {
		t.Fatalf("run-a2 = %#v", second)
	}
	if _, err := db.ListRunUsage(ctx, until, since); err == nil {
		t.Fatal("expected invalid range error")
	}
}
