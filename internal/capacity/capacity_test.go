package capacity_test

import (
	"math"
	"testing"
	"time"

	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/decision"
)

func TestEstimateCorrelatesTokenUsageWithQuantizedWindowDrain(t *testing.T) {
	start := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	shortReset := start.Add(5 * time.Hour)
	weeklyReset := start.Add(4 * 24 * time.Hour)
	snapshots := []decision.UsageSnapshot{
		snapshot(start, 1.00, .80, shortReset, weeklyReset),
		snapshot(start.Add(time.Minute), 1.00, .80, shortReset, weeklyReset),
		snapshot(start.Add(2*time.Minute), .98, .79, shortReset, weeklyReset),
		snapshot(start.Add(3*time.Minute), .95, .78, shortReset, weeklyReset),
	}
	observations := []capacity.TokenObservation{
		{Provider: "claude", ObservedAt: start.Add(30 * time.Second), InputTokens: 600, OutputTokens: 400},
		{Provider: "claude", ObservedAt: start.Add(90 * time.Second), InputTokens: 1200, OutputTokens: 800},
		{Provider: "claude", ObservedAt: start.Add(150 * time.Second), InputTokens: 1200, OutputTokens: 800},
	}

	got := capacity.Estimate("claude", snapshots, observations, .10, start.Add(time.Hour))
	if got.Short == nil || got.Weekly == nil {
		t.Fatalf("expected both estimates: %#v", got)
	}
	// 5,000 measured tokens / 5%% short drain = 100,000 tokens.
	if math.Abs(got.Short.EstimatedTokens.Total-100_000) > .01 {
		t.Fatalf("short total = %f", got.Short.EstimatedTokens.Total)
	}
	// 5,000 measured tokens / 2%% weekly drain = 250,000 tokens.
	if math.Abs(got.Weekly.EstimatedTokens.Total-250_000) > .01 {
		t.Fatalf("weekly total = %f", got.Weekly.EstimatedTokens.Total)
	}
	if got.Short.ClosedSpans != 2 || got.Weekly.ClosedSpans != 2 {
		t.Fatalf("closed spans short=%d weekly=%d", got.Short.ClosedSpans, got.Weekly.ClosedSpans)
	}
	if math.Abs(*got.RatioDerivedWeeklyTokens-1_000_000) > .01 {
		t.Fatalf("ratio-derived weekly = %f", *got.RatioDerivedWeeklyTokens)
	}
}

func TestEstimateDoesNotCrossResetBoundariesOrMixProviders(t *testing.T) {
	start := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	weeklyReset := start.Add(4 * 24 * time.Hour)
	snapshots := []decision.UsageSnapshot{
		snapshot(start, .20, .80, start.Add(time.Hour), weeklyReset),
		snapshot(start.Add(time.Hour), 1.00, .79, start.Add(6*time.Hour), weeklyReset),
		snapshot(start.Add(2*time.Hour), .90, .78, start.Add(6*time.Hour), weeklyReset),
	}
	observations := []capacity.TokenObservation{
		{Provider: "claude", ObservedAt: start.Add(30 * time.Minute), InputTokens: 99_000},
		{Provider: "codex", ObservedAt: start.Add(90 * time.Minute), InputTokens: 99_000},
		{Provider: "claude", ObservedAt: start.Add(90 * time.Minute), InputTokens: 1_000},
	}

	got := capacity.Estimate("claude", snapshots, observations, .10, start.Add(3*time.Hour))
	if got.Short == nil || got.Short.MeasuredTokens.Total != 1_000 {
		t.Fatalf("short estimate included reset/provider tokens: %#v", got.Short)
	}
}

func TestEstimateRequiresTokenAndPercentageEvidence(t *testing.T) {
	start := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	reset := start.Add(5 * time.Hour)
	weekly := start.Add(7 * 24 * time.Hour)
	got := capacity.Estimate("claude", []decision.UsageSnapshot{
		snapshot(start, 1, 1, reset, weekly), snapshot(start.Add(time.Minute), .9, .99, reset, weekly),
	}, nil, .1, start.Add(time.Hour))
	if got.Short != nil || got.Weekly != nil || got.Confidence != capacity.ConfidenceInsufficient {
		t.Fatalf("unexpected estimate: %#v", got)
	}
}

func snapshot(at time.Time, short, weekly float64, shortReset, weeklyReset time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{Provider: "claude", ObservedAt: at,
		Short:  &decision.UsageWindow{Remaining: short, ResetsAt: shortReset},
		Weekly: decision.UsageWindow{Remaining: weekly, ResetsAt: weeklyReset}}
}
