package calibration_test

import (
	"math"
	"testing"
	"time"

	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/decision"
)

func TestEstimateUsesObservedRatioAfterTwoInformativeWindows(t *testing.T) {
	weeklyReset := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	snapshots := []decision.UsageSnapshot{
		snapshot(1.00, 0.80, time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC), weeklyReset),
		snapshot(0.40, 0.75, time.Date(2026, 7, 18, 5, 0, 1, 0, time.UTC), weeklyReset.Add(300*time.Millisecond)),
		snapshot(0.90, 0.75, time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC), weeklyReset),
		snapshot(0.40, 0.71, time.Date(2026, 7, 18, 9, 59, 59, 0, time.UTC), weeklyReset.Add(-300*time.Millisecond)),
	}

	got := calibration.EstimateWindowWeeklyCost("claude", snapshots, 0.08, weeklyReset)
	want := 0.09 / 1.10
	if got.ObservedCost == nil || math.Abs(*got.ObservedCost-want) > 0.000001 {
		t.Fatalf("observed cost = %v, want %.6f", got.ObservedCost, want)
	}
	if math.Abs(got.EffectiveCost-want) > 0.000001 || got.Source != calibration.SourceObserved || got.Confidence != calibration.ConfidenceMedium {
		t.Fatalf("estimate = %#v", got)
	}
	if got.InformativeWindows != 2 || math.Abs(got.TotalShortUsage-1.10) > 0.000001 {
		t.Fatalf("estimate evidence = %#v", got)
	}
}

func TestEstimateRetainsConfiguredFallbackUntilEvidenceIsSufficient(t *testing.T) {
	weeklyReset := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	snapshots := []decision.UsageSnapshot{
		snapshot(0.80, 0.60, time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC), weeklyReset),
		snapshot(0.60, 0.58, time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC), weeklyReset),
	}

	got := calibration.EstimateWindowWeeklyCost("claude", snapshots, 0.08, weeklyReset)
	if got.ObservedCost == nil || math.Abs(*got.ObservedCost-0.10) > 0.000001 {
		t.Fatalf("observed cost = %v", got.ObservedCost)
	}
	if got.EffectiveCost != 0.08 || got.Source != calibration.SourceConfigured || got.Confidence != calibration.ConfidenceLow {
		t.Fatalf("estimate = %#v", got)
	}
}

func TestEstimateCannotLearnWithoutShortWindow(t *testing.T) {
	weeklyReset := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	snapshots := []decision.UsageSnapshot{{
		Provider: "codex", ObservedAt: weeklyReset.Add(-time.Hour),
		Weekly: decision.UsageWindow{Remaining: 0.50, ResetsAt: weeklyReset},
	}}

	got := calibration.EstimateWindowWeeklyCost("codex", snapshots, 0.10, weeklyReset)
	if got.ObservedCost != nil || got.EffectiveCost != 0.10 || got.Confidence != calibration.ConfidenceInsufficient {
		t.Fatalf("estimate = %#v", got)
	}
}

func snapshot(shortRemaining, weeklyRemaining float64, shortReset, weeklyReset time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider: "claude", ObservedAt: shortReset.Add(-time.Hour),
		Short:  &decision.UsageWindow{Remaining: shortRemaining, ResetsAt: shortReset},
		Weekly: decision.UsageWindow{Remaining: weeklyRemaining, ResetsAt: weeklyReset},
	}
}
