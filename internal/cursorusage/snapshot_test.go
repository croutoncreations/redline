package cursorusage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/openusage"
)

// The recorded OpenUsage card: Cursor Models 0%, Other Models 100% (used up),
// Total 4.8%, Grok Bot 58%, credits $22.23.
func recordedReport(t *testing.T) Report {
	t.Helper()
	raw, err := os.ReadFile("testdata/openusage-cursor-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := openusage.ParseLimits(raw)
	if err != nil {
		t.Fatal(err)
	}
	report, err := FromLimits(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestSnapshotUsesOnlyMonthlySubscriptionPools(t *testing.T) {
	report := recordedReport(t)
	snapshot, err := Snapshot(report, report.FetchedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "cursor" || snapshot.Short != nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	cursorPool, ok := snapshot.Allowance("model:" + config.CursorModelsGroup + ":monthly")
	if !ok || cursorPool.Role != "monthly" || cursorPool.Remaining != 1 || cursorPool.PeriodDurationSeconds != 2592000 {
		t.Fatalf("cursor pool = %#v", cursorPool)
	}
	other, ok := snapshot.Allowance("model:" + config.CursorOtherModelsGroup + ":monthly")
	if !ok || other.Remaining != 0 {
		t.Fatalf("other pool = %#v; Other Models is used up", other)
	}
	// The account window is Cursor's own total, measured over the month.
	if snapshot.Weekly.Period() != 30*24*time.Hour || snapshot.Weekly.Remaining < 0.95 || snapshot.Weekly.Remaining > 0.96 {
		t.Fatalf("account window = %#v", snapshot.Weekly)
	}
	for _, allowance := range snapshot.Allowances {
		if allowance.Key == "model:grokBot:weekly" || allowance.SourceLabel == "Grok Bot" || allowance.SourceLabel == "On-demand" {
			t.Fatalf("Grok Bot and on-demand must never be scheduling allowances: %#v", allowance)
		}
	}
}

// On-demand spend is paid overage, not quota. It must not turn into
// headroom even when it is the only thing left.
func TestSnapshotIgnoresOnDemandAndCredits(t *testing.T) {
	now := fixedNow
	reset := now.Add(10 * 24 * time.Hour)
	limit := 100.0
	report := newReport()
	report.Source = SourceOpenUsage
	report.FetchedAt = &now
	report.Meters = []Meter{
		{Key: "autoUsage", Label: "Cursor Models", Group: GroupMonthly, Unit: "percent", Used: 100, Limit: &limit, ResetsAt: &reset},
		{Key: "apiUsage", Label: "Other Models", Group: GroupMonthly, Unit: "percent", Used: 100, Limit: &limit, ResetsAt: &reset},
		{Key: "onDemand", Label: "On-demand", Group: GroupOnDemand, Unit: "usd", Used: 1, Limit: floatPointer(200), ResetsAt: &reset},
	}
	report.Balances = []Balance{{Key: "credits", Label: "Credits", Unit: "usd", Available: 50}}
	snapshot, err := Snapshot(report, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Weekly.Remaining != 0 {
		t.Fatalf("both pools are used up; on-demand and credits must not count: %#v", snapshot.Weekly)
	}
}

func TestSnapshotFailsClosedWithoutPools(t *testing.T) {
	now := fixedNow
	report := newReport()
	report.FetchedAt = &now
	if _, err := Snapshot(report, now); err == nil {
		t.Fatal("a report without monthly pools must not schedule")
	}
	if _, err := Snapshot(newReport(), now); err == nil {
		t.Fatal("an unread report must not schedule")
	}
}

// Pace is measured against the month: 50% left with 15 of 30 days to go is
// on pace, not "well behind" as it would be against a 7-day week.
func TestMonthlyWindowPacesAgainstItsOwnPeriod(t *testing.T) {
	now := fixedNow
	window := decision.UsageWindow{Remaining: 0.5, ResetsAt: now.Add(15 * 24 * time.Hour), PeriodSeconds: 30 * 24 * 3600}
	trigger := 0.15
	result := decision.Evaluate(decision.Input{
		Snapshot:         decision.UsageSnapshot{Provider: "cursor", ObservedAt: now, Weekly: window},
		WindowWeeklyCost: 0.1, PaceGapTrigger: &trigger, Now: now, MaxSnapshotAge: time.Hour,
	})
	if result.Decision != decision.Wait || result.PaceGap > 0.01 || result.PaceGap < -0.01 {
		t.Fatalf("result = %#v", result)
	}
	window.Remaining = 0.8
	result = decision.Evaluate(decision.Input{
		Snapshot:         decision.UsageSnapshot{Provider: "cursor", ObservedAt: now, Weekly: window},
		WindowWeeklyCost: 0.1, PaceGapTrigger: &trigger, Now: now, MaxSnapshotAge: time.Hour,
	})
	if result.Decision != decision.Admit {
		t.Fatalf("80%% left at mid-month is behind pace: %#v", result)
	}
}

type fixedFetcher struct{ report Report }

func (f fixedFetcher) Fetch(context.Context) (Report, error) { return f.report, nil }

// The snapshot is stored under the name the usage manager records as the
// active source, or the dashboard's per-source lookup would not find it.
func TestUsageSourceLabelsSnapshotWithItsOwnName(t *testing.T) {
	report := recordedReport(t)
	report.Source = SourceNative
	source := UsageSource{SourceName: "native", Now: func() time.Time { return report.FetchedAt.Add(time.Minute) },
		Collector: func(config.Provider) Fetcher { return fixedFetcher{report} }}
	snapshot, _, err := source.Fetch(context.Background(), config.Provider{Provider: "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != "native" {
		t.Fatalf("source = %q, want native", snapshot.Source)
	}
}
