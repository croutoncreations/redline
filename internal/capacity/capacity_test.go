package capacity_test

import (
	"fmt"
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
	if got.RatioDerivedDifference == nil || math.Abs(*got.RatioDerivedDifference-3) > .01 {
		t.Fatalf("ratio-derived difference = %#v", got.RatioDerivedDifference)
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

func TestEstimateReportsWeightedAllowanceAndPricingCoverage(t *testing.T) {
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	shortReset, weeklyReset := start.Add(5*time.Hour), start.Add(5*24*time.Hour)
	snapshots := []decision.UsageSnapshot{
		snapshot(start, 1, .8, shortReset, weeklyReset),
		snapshot(start.Add(time.Minute), .95, .78, shortReset, weeklyReset),
	}
	observations := []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost-pi", SourceID: "1", Model: "claude-fable-5", ObservedAt: start.Add(20 * time.Second), InputTokens: 1_000_000, Confidence: "high"},
		{Provider: "claude", Source: "gatepost-pi", SourceID: "2", Model: "claude-haiku-4-5", ObservedAt: start.Add(40 * time.Second), InputTokens: 1_000_000, Confidence: "high"},
	}
	got := capacity.Estimate("claude", snapshots, observations, .08, start.Add(time.Hour))
	if got.Short == nil || got.Short.Accounting == nil {
		t.Fatalf("missing accounting estimate: %#v", got.Short)
	}
	weighted := got.Short.Accounting
	if weighted.Unit != "usd_api_equivalent" || math.Abs(weighted.EstimatedCapacityLow-220) > .000001 ||
		math.Abs(weighted.EstimatedCapacityHigh-220) > .000001 || weighted.PricingCoverage != 1 || weighted.PricedObservations != 2 {
		t.Fatalf("weighted = %#v", weighted)
	}
	if got.Weekly == nil || got.Weekly.Accounting == nil || math.Abs(got.Weekly.Accounting.EstimatedCapacityLow-550) > .000001 {
		t.Fatalf("weekly accounting = %#v", got.Weekly)
	}
}

func TestEstimateReportsUnknownModelAsUnpricedCoverage(t *testing.T) {
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	reset, weekly := start.Add(5*time.Hour), start.Add(5*24*time.Hour)
	got := capacity.Estimate("claude", []decision.UsageSnapshot{
		snapshot(start, 1, 1, reset, weekly), snapshot(start.Add(time.Minute), .9, .99, reset, weekly),
	}, []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost-pi", SourceID: "known", Model: "claude-haiku-4-5", ObservedAt: start.Add(20 * time.Second), InputTokens: 100},
		{Provider: "claude", Source: "gatepost-pi", SourceID: "unknown", Model: "private", ObservedAt: start.Add(30 * time.Second), InputTokens: 100},
	}, .08, start.Add(time.Hour))
	if got.Short.Accounting.PricingCoverage != .5 || got.Short.Accounting.UnpricedObservations != 1 || len(got.Short.Accounting.UnpricedModels) != 1 || got.Short.Accounting.UnpricedModels[0] != "private" {
		t.Fatalf("accounting = %#v", got.Short.Accounting)
	}
}

func TestEstimateReportsAttributionCoverageAndEvidenceComposition(t *testing.T) {
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	reset, weekly := start.Add(5*time.Hour), start.Add(5*24*time.Hour)
	got := capacity.Estimate("claude", []decision.UsageSnapshot{
		snapshot(start, 1, 1, reset, weekly),
		snapshot(start.Add(time.Minute), .99, .99, reset, weekly),
		snapshot(start.Add(2*time.Minute), .98, .98, reset, weekly),
	}, []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost-pi", SourceID: "pi-1", Model: "claude-opus-4-8",
			ObservedAt: start.Add(20 * time.Second), InputTokens: 10, OutputTokens: 5, CacheReadTokens: 60,
			CacheCreationTokens: 5, Confidence: "high"},
		{Provider: "claude", Source: "gatepost", SourceID: "direct-1", Model: "claude-sonnet-5",
			ObservedAt: start.Add(40 * time.Second), InputTokens: 15, OutputTokens: 5, Confidence: "medium"},
	}, .08, start.Add(time.Hour))

	if got.Short == nil {
		t.Fatal("missing short estimate")
	}
	if math.Abs(got.Short.TotalObservedUsage-.02) > 1e-9 ||
		math.Abs(got.Short.UnattributedUsage-.01) > 1e-9 ||
		math.Abs(got.Short.AttributionCoverage-.5) > 1e-9 || got.Short.UnattributedSpans != 1 {
		t.Fatalf("attribution = %#v", got.Short)
	}
	if got.Short.Confidence != capacity.ConfidenceLow {
		t.Fatalf("confidence = %q", got.Short.Confidence)
	}
	if len(got.Short.Sources) != 2 || got.Short.Sources[0].Key != "gatepost-pi" ||
		got.Short.Sources[0].Tokens.Total != 80 || got.Short.Sources[0].FractionOfMeasuredTokens != .8 {
		t.Fatalf("sources = %#v", got.Short.Sources)
	}
	if len(got.Short.Models) != 2 || got.Short.Models[0].Key != "claude-opus-4-8" ||
		got.Short.Models[0].Observations != 1 || got.Short.Models[0].Tokens.CacheRead != 60 {
		t.Fatalf("models = %#v", got.Short.Models)
	}
}

func TestAttributionCoverageCapsOtherwiseHighConfidence(t *testing.T) {
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	reset, weekly := start.Add(5*time.Hour), start.Add(5*24*time.Hour)
	snapshots := make([]decision.UsageSnapshot, 0, 14)
	observations := make([]capacity.TokenObservation, 0, 12)
	for index := range 14 {
		remaining := 1 - float64(index)*.02
		snapshots = append(snapshots, snapshot(start.Add(time.Duration(index)*time.Minute), remaining, remaining, reset, weekly))
		if index > 0 && index <= 8 {
			observations = append(observations, capacity.TokenObservation{
				Provider: "claude", Source: "gatepost-pi", SourceID: fmt.Sprintf("%d", index),
				Model: "claude-opus-4-8", ObservedAt: start.Add(time.Duration(index)*time.Minute - time.Second),
				InputTokens: 1_000, Confidence: "high",
			})
		}
	}
	got := capacity.Estimate("claude", snapshots, observations, .08, start.Add(time.Hour))
	if got.Short == nil || got.Short.ClosedSpans != 8 || got.Short.ObservedSpans != 13 ||
		got.Short.Confidence != capacity.ConfidenceLow {
		t.Fatalf("short = %#v", got.Short)
	}
}

func TestOverallConfidenceUsesWeakestAvailableWindow(t *testing.T) {
	start := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	reset, weeklyReset := start.Add(5*time.Hour), start.Add(5*24*time.Hour)
	var snapshots []decision.UsageSnapshot
	var observations []capacity.TokenObservation
	for index := range 14 {
		snapshots = append(snapshots, snapshot(
			start.Add(time.Duration(index)*time.Minute),
			1-float64(index)*.03,
			1-float64(index)*.005,
			reset, weeklyReset,
		))
		if index > 0 {
			observations = append(observations, capacity.TokenObservation{
				Provider: "claude", Source: "gatepost-pi", SourceID: fmt.Sprintf("%d", index),
				Model: "claude-opus-4-8", ObservedAt: start.Add(time.Duration(index)*time.Minute - time.Second),
				InputTokens: 1_000, Confidence: "high",
			})
		}
	}
	got := capacity.Estimate("claude", snapshots, observations, .08, start.Add(time.Hour))
	if got.Short == nil || got.Short.Confidence != capacity.ConfidenceHigh ||
		got.Weekly == nil || got.Weekly.Confidence != capacity.ConfidenceLow ||
		got.Confidence != capacity.ConfidenceLow {
		t.Fatalf("estimate = %#v", got)
	}
}

func snapshot(at time.Time, short, weekly float64, shortReset, weeklyReset time.Time) decision.UsageSnapshot {
	return decision.UsageSnapshot{Provider: "claude", ObservedAt: at,
		Short:  &decision.UsageWindow{Remaining: short, ResetsAt: shortReset},
		Weekly: decision.UsageWindow{Remaining: weekly, ResetsAt: weeklyReset}}
}
