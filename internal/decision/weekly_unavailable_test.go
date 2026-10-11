package decision

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func teamSnapshot(now time.Time) UsageSnapshot {
	return UsageSnapshot{
		Provider: "claude", ObservedAt: now, Source: "openusage", Confidence: "high", Plan: "Team 5x",
		WeeklyUnavailable: true, ShortWindowNotStarted: true,
		Allowances: []AllowanceWindow{{Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly",
			Remaining: 1, ResetsAt: now.Add(40 * time.Hour), PeriodDurationSeconds: WeekSeconds}},
	}
}

func TestWeeklyNotReportedIsValidAndHoldsDispatch(t *testing.T) {
	now := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)
	snapshot := teamSnapshot(now)
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	result := Evaluate(Input{Snapshot: snapshot, WindowWeeklyCost: .08, TriggerMargin: .02, RollingReserve: .25,
		PaceThresholds: []PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: .5}}, Now: now.Add(time.Minute),
		MaxSnapshotAge: 15 * time.Minute})
	if result.Decision != Unknown || !strings.Contains(result.Reason, "weekly limit not reported") {
		t.Fatalf("result = %#v", result)
	}
	if ProjectTriggerAt(Input{Snapshot: snapshot, Now: now}, 5*time.Minute) != nil {
		t.Fatal("no trigger can be projected without a weekly window")
	}
	if _, ok := snapshot.Allowance("weekly"); ok {
		t.Fatal("an unreported weekly window must not appear as an allowance")
	}
	for _, allowance := range snapshot.AllAllowances() {
		if allowance.Key == "weekly" {
			t.Fatal("AllAllowances invented a weekly window")
		}
	}
}

func TestWeeklyNotReportedNeverCarriesANumber(t *testing.T) {
	now := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)
	bad := teamSnapshot(now)
	bad.Weekly = UsageWindow{Remaining: .76, ResetsAt: now.Add(3 * time.Hour)}
	if err := bad.Validate(); err == nil {
		t.Fatal("a value beside 'not reported' must be refused")
	}
	empty := UsageSnapshot{Provider: "claude", ObservedAt: now, WeeklyUnavailable: true}
	if err := empty.Validate(); err == nil {
		t.Fatal("a snapshot reporting nothing at all must be refused")
	}
	encoded, err := json.Marshal(teamSnapshot(now))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if _, present := fields["weekly"]; present || fields["weekly_unavailable"] != true {
		t.Fatalf("encoded = %s", encoded)
	}
	ordinary := teamSnapshot(now)
	ordinary.WeeklyUnavailable, ordinary.Weekly = false, UsageWindow{Remaining: .5, ResetsAt: now.Add(time.Hour)}
	encoded, _ = json.Marshal(ordinary)
	if !strings.Contains(string(encoded), `"weekly":{"remaining":0.5`) || strings.Contains(string(encoded), "weekly_unavailable") {
		t.Fatalf("ordinary snapshot changed shape: %s", encoded)
	}
}
