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
	for _, attempt := range []domain.DispatchAttempt{
		{ProviderAccountID: "codex-main", Trigger: "automatic", Outcome: domain.DispatchError,
			Error: "OpenUsage unavailable", StartedAt: start, CompletedAt: start.Add(time.Second)},
		{ProviderAccountID: "codex-main", Trigger: "manual", Outcome: domain.DispatchWait,
			Decision: "WAIT", Mode: "pace_threshold", Reason: "no threshold matched",
			StartedAt: start.Add(time.Minute), CompletedAt: start.Add(time.Minute + time.Second)},
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
}

func TestDispatchAttemptValidation(t *testing.T) {
	db := openTaskDB(t)
	_, err := db.RecordDispatchAttempt(context.Background(), domain.DispatchAttempt{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
