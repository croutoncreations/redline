package decision_test

import (
	"testing"
	"time"

	"github.com/jfox/redline/internal/decision"
)

var now = time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)

func limitedSnapshot() decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider:   "claude",
		ObservedAt: now,
		Short: &decision.UsageWindow{
			Remaining: 0.80,
			ResetsAt:  now.Add(2 * time.Hour),
		},
		Weekly: decision.UsageWindow{
			Remaining: 0.47,
			ResetsAt:  now.Add(11 * time.Hour),
		},
		Source: "test",
	}
}

func input(s decision.UsageSnapshot) decision.Input {
	return decision.Input{
		Snapshot:         s,
		WindowWeeklyCost: 0.10,
		TriggerMargin:    0.02,
		RollingReserve:   0.25,
		Now:              now,
		MaxSnapshotAge:   15 * time.Minute,
	}
}

func TestEvaluateProratesCurrentAndFinalSlots(t *testing.T) {
	got := decision.Evaluate(input(limitedSnapshot()))

	if got.Decision != decision.Run {
		t.Fatalf("decision = %q, want RUN (reason: %s)", got.Decision, got.Reason)
	}
	wantFractions := []float64{0.4, 1, 0.8}
	if len(got.Slots) != len(wantFractions) {
		t.Fatalf("slots = %#v", got.Slots)
	}
	for i, want := range wantFractions {
		assertClose(t, "slot fraction", got.Slots[i].Fraction, want)
	}
	assertClose(t, "maximum consumable", got.MaximumConsumable, 0.22)
	assertClose(t, "overflow", got.Overflow, 0.25)
}

func TestCurrentSlotUsesLowerOfTimeAndRemainingAllowance(t *testing.T) {
	s := limitedSnapshot()
	s.Short.Remaining = 0.15
	got := decision.Evaluate(input(s))

	assertClose(t, "current slot", got.Slots[0].Fraction, 0.15)
}

func TestFinalSlotAtWeeklyBoundaryIsIncluded(t *testing.T) {
	s := limitedSnapshot()
	s.Short.ResetsAt = now.Add(5 * time.Hour)
	s.Weekly.ResetsAt = now.Add(5*time.Hour + time.Minute)
	got := decision.Evaluate(input(s))

	if len(got.Slots) != 2 {
		t.Fatalf("slot count = %d, want 2", len(got.Slots))
	}
	assertClose(t, "final fraction", got.Slots[1].Fraction, 1.0/300.0)
}

func TestLimitedWindowWaitsAtRollingReserve(t *testing.T) {
	s := limitedSnapshot()
	s.Short.Remaining = 0.25
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Wait || got.Reason != "current 5-hour reserve protected" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestExhaustedCurrentWindowDoesNotMisclassifyFirstFutureSlot(t *testing.T) {
	s := limitedSnapshot()
	s.Short.Remaining = 0
	got := decision.Evaluate(input(s))

	if got.FutureFullWindows != 1 {
		t.Fatalf("future full windows = %d, want 1", got.FutureFullWindows)
	}
	assertClose(t, "current capacity", got.CurrentWindowCapacity, 0)
	if len(got.Slots) == 0 || got.Slots[0].Current {
		t.Fatalf("first slot = %#v, want future slot", got.Slots)
	}
}

func TestUnrestrictedProviderRunsWhenPaceThresholdMatches(t *testing.T) {
	s := limitedSnapshot()
	s.Provider = "codex"
	s.Short = nil
	s.Weekly.Remaining = 0.60
	s.Weekly.ResetsAt = now.Add(48 * time.Hour)
	in := input(s)
	in.PaceThresholds = []decision.PaceThreshold{
		{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50},
		{TimeRemaining: 24 * time.Hour, MinWeeklyRemaining: 0.20},
	}
	got := decision.Evaluate(in)

	if got.Decision != decision.Run || got.Mode != decision.ModePace {
		t.Fatalf("got %#v, want pace RUN", got)
	}
	if got.MatchedPaceThreshold == nil || got.MatchedPaceThreshold.MinWeeklyRemaining != 0.50 {
		t.Fatalf("matched threshold = %#v", got.MatchedPaceThreshold)
	}
}

func TestUnrestrictedProviderWaitsWithoutMatchingThreshold(t *testing.T) {
	s := limitedSnapshot()
	s.Provider = "codex"
	s.Short = nil
	s.Weekly.Remaining = 0.40
	s.Weekly.ResetsAt = now.Add(48 * time.Hour)
	in := input(s)
	in.PaceThresholds = []decision.PaceThreshold{
		{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50},
	}
	got := decision.Evaluate(in)

	if got.Decision != decision.Wait || got.Reason != "no pace threshold matched" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestUnrestrictedProviderWithoutThresholdsFailsClosed(t *testing.T) {
	s := limitedSnapshot()
	s.Short = nil
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Unknown || got.Reason != "unrestricted provider has no pace thresholds" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestEvaluateFailsClosedForStaleSnapshot(t *testing.T) {
	s := limitedSnapshot()
	s.ObservedAt = now.Add(-16 * time.Minute)
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Unknown || got.Reason != "usage snapshot is stale" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestEvaluateFailsClosedForExpiredWeeklyReset(t *testing.T) {
	s := limitedSnapshot()
	s.Weekly.ResetsAt = now
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Unknown || got.Reason != "weekly reset is not in the future" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestUsageSnapshotValidateRejectsFractionsOutsideUnitInterval(t *testing.T) {
	for _, value := range []float64{-0.01, 1.01} {
		s := limitedSnapshot()
		s.Weekly.Remaining = value
		if err := s.Validate(); err == nil {
			t.Errorf("weekly remaining %v: expected validation error", value)
		}
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	const epsilon = 1e-9
	if got < want-epsilon || got > want+epsilon {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
