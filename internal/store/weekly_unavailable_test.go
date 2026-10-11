package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

// A Team seat's snapshot (no weekly window) is stored and loads back with
// weekly still "not reported" -- the zero window kept in the NOT NULL
// columns never comes back as a 0% weekly value.
func TestSnapshotWithoutAWeeklyWindowRoundTrips(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 10, 22, 14, 21, 0, time.UTC)
	zero := 0
	snapshot := decision.UsageSnapshot{
		Provider: "claude", Account: "claude-5ea70003", ObservedAt: now, Source: "openusage", Confidence: "high",
		Plan: "Team 5x", WeeklyUnavailable: true, BankedResets: &zero,
		Short: &decision.UsageWindow{Remaining: .76, ResetsAt: now.Add(155 * time.Minute)},
		Allowances: []decision.AllowanceWindow{
			{Key: "session", SourceLabel: "Session", Scope: "account", Role: "short", Remaining: .76,
				ResetsAt: now.Add(155 * time.Minute), PeriodDurationSeconds: 18000},
			{Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly", Remaining: 1,
				ResetsAt: now.Add(5 * 24 * time.Hour), PeriodDurationSeconds: decision.WeekSeconds},
		},
	}
	if err := db.SaveSnapshot(context.Background(), snapshot, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := db.LatestSnapshot(context.Background(), "claude-5ea70003", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.WeeklyUnavailable || loaded.Weekly != (decision.UsageWindow{}) {
		t.Fatalf("weekly = %#v unavailable=%v", loaded.Weekly, loaded.WeeklyUnavailable)
	}
	if loaded.Short == nil || loaded.Short.Remaining != .76 || len(loaded.Allowances) != 2 || loaded.Plan != "Team 5x" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListSnapshots(context.Background(), "claude-5ea70003", "claude", 10)
	if err != nil || len(list) != 1 || !list[0].WeeklyUnavailable || list[0].Weekly != (decision.UsageWindow{}) {
		t.Fatalf("list = %#v err=%v", list, err)
	}
}
