package decision_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
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

	if got.Decision != decision.Admit {
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
	if got.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("unlocked tier = %q", got.UnlockedTier)
	}
}

func TestLimitedWindowCanRunBehindPaceBeforeOverflow(t *testing.T) {
	s := limitedSnapshot()
	s.Weekly.Remaining = 0.50
	s.Weekly.ResetsAt = now.Add(48 * time.Hour)
	s.Short.ResetsAt = now.Add(2 * time.Hour)
	in := input(s)
	in.WindowWeeklyCost = 0.10
	in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50}}
	got := decision.Evaluate(in)

	if got.Decision != decision.Admit || got.Overflow > in.TriggerMargin || got.UnlockedTier != domain.DispatchWellBehind {
		t.Fatalf("got %#v", got)
	}
}

func TestLimitedWindowRunsWhenPolicyPaceGapTriggerMatches(t *testing.T) {
	s := limitedSnapshot()
	s.Weekly.Remaining = 0.90
	s.Weekly.ResetsAt = now.Add(95 * time.Hour)
	in := input(s)
	in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50}}
	trigger := 0.30
	in.PaceGapTrigger = &trigger

	got := decision.Evaluate(in)

	if got.Decision != decision.Admit || got.Reason != "weekly remaining is well behind pace" {
		t.Fatalf("got %#v", got)
	}
	if got.MatchedPaceThreshold != nil || got.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("got %#v", got)
	}
}

func TestLimitedWindowKeepsPaceGapAdmissionDisabledWhenPolicyOmitsIt(t *testing.T) {
	s := limitedSnapshot()
	s.Weekly.Remaining = 0.90
	s.Weekly.ResetsAt = now.Add(95 * time.Hour)
	in := input(s)
	in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50}}

	got := decision.Evaluate(in)

	if got.Decision != decision.Wait || got.Reason != "no actionable weekly overflow" {
		t.Fatalf("got %#v", got)
	}
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

func TestWeeklyResetBeforeShortResetProducesOnlyProratedCurrentSlot(t *testing.T) {
	s := limitedSnapshot()
	s.Short.ResetsAt = now.Add(4 * time.Hour)
	s.Short.Remaining = 1
	s.Weekly.ResetsAt = now.Add(30 * time.Minute)
	got := decision.Evaluate(input(s))

	if len(got.Slots) != 1 || !got.Slots[0].Current {
		t.Fatalf("slots = %#v", got.Slots)
	}
	assertClose(t, "current fraction", got.Slots[0].Fraction, 0.1)
}

func TestAlignedWeeklyAndShortResetDoesNotCreateZeroLengthFinalSlot(t *testing.T) {
	s := limitedSnapshot()
	s.Short.ResetsAt = now.Add(5 * time.Hour)
	s.Short.Remaining = 1
	s.Weekly.ResetsAt = s.Short.ResetsAt
	got := decision.Evaluate(input(s))

	if len(got.Slots) != 1 || !got.Slots[0].Current || !got.Slots[0].EndsAt.Equal(s.Weekly.ResetsAt) {
		t.Fatalf("slots = %#v", got.Slots)
	}
}

func TestLimitedWindowWaitsAtRollingReserve(t *testing.T) {
	s := limitedSnapshot()
	s.Short.Remaining = 0.25
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Wait || got.Reason != "current 5-hour reserve protected" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestLimitedWindowWaitsWhenOverflowEqualsTriggerMargin(t *testing.T) {
	s := limitedSnapshot()
	in := input(s)
	baseline := decision.Evaluate(in)
	s.Weekly.Remaining = baseline.MaximumConsumable + in.TriggerMargin
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Wait || got.Reason != "no actionable weekly overflow" {
		t.Fatalf("got %q (%s), overflow=%v", got.Decision, got.Reason, got.Overflow)
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

	if got.Decision != decision.Admit || got.Mode != decision.ModePace {
		t.Fatalf("got %#v, want pace RUN", got)
	}
	if got.MatchedPaceThreshold == nil || got.MatchedPaceThreshold.MinWeeklyRemaining != 0.50 {
		t.Fatalf("matched threshold = %#v", got.MatchedPaceThreshold)
	}
	if got.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("unlocked tier = %q", got.UnlockedTier)
	}
}

func TestUnrestrictedProviderRunsWhenPaceGapTriggerMatches(t *testing.T) {
	s := limitedSnapshot()
	s.Short = nil
	s.Weekly.Remaining = 0.90
	s.Weekly.ResetsAt = now.Add(95 * time.Hour)
	in := input(s)
	in.PaceThresholds = nil
	trigger := 0.30
	in.PaceGapTrigger = &trigger

	got := decision.Evaluate(in)

	if got.Decision != decision.Admit || got.Mode != decision.ModePace ||
		got.Reason != "weekly remaining is well behind pace" ||
		got.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("got %#v", got)
	}
}

func TestUnrestrictedPaceThresholdIsInclusiveAtExactBoundaries(t *testing.T) {
	s := limitedSnapshot()
	s.Provider = "codex"
	s.Short = nil
	s.Weekly.Remaining = 0.50
	s.Weekly.ResetsAt = now.Add(72 * time.Hour)
	in := input(s)
	in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: 0.50}}
	got := decision.Evaluate(in)

	if got.Decision != decision.Admit || got.Mode != decision.ModePace {
		t.Fatalf("got %#v", got)
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

// A weekly-only account (verified Claude Ultra, or Codex without its short
// limit) is budgeted on the weekly window alone: leftover quota before reset
// is filled, an empty week waits, and nothing waits on a 5-hour reset.
func TestWeeklyOnlyAccountFillsLeftoverBeforeResetAndWaitsWhenEmpty(t *testing.T) {
	gap, firesale := .30, .45
	weeklyOnly := func(remaining float64, untilReset time.Duration) decision.Input {
		s := limitedSnapshot()
		s.Short = nil
		s.Weekly = decision.UsageWindow{Remaining: remaining, ResetsAt: now.Add(untilReset)}
		in := input(s)
		in.PaceGapTrigger, in.FiresaleTrigger = &gap, &firesale
		in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 24 * time.Hour, MinWeeklyRemaining: .20}}
		return in
	}

	leftover := decision.Evaluate(weeklyOnly(.60, 12*time.Hour))
	if leftover.Decision != decision.Admit || leftover.Mode != decision.ModePace || !leftover.Firesale ||
		leftover.UnlockedTier != domain.DispatchFiresale || len(leftover.Slots) != 0 {
		t.Fatalf("leftover weekly quota near reset should be filled: %#v", leftover)
	}
	forced := weeklyOnly(.05, 12*time.Hour)
	forced.ForceFiresale = true
	if got := decision.Evaluate(forced); got.Decision != decision.Admit || !got.Firesale {
		t.Fatalf("announcement firesale should run a weekly-only account with quota left: %#v", got)
	}
	empty := weeklyOnly(0, 12*time.Hour)
	empty.ForceFiresale = true
	if got := decision.Evaluate(empty); got.Decision != decision.Wait {
		t.Fatalf("an exhausted weekly window must wait: %#v", got)
	}
	onPace := weeklyOnly(.50, 84*time.Hour)
	if got := decision.Evaluate(onPace); got.Decision != decision.Wait || got.Mode != decision.ModePace {
		t.Fatalf("on-pace weekly-only account should wait: %#v", got)
	}
	if at := decision.ProjectTriggerAt(onPace, time.Hour); at == nil || !at.After(now) || !at.Before(now.Add(84*time.Hour)) {
		t.Fatalf("flat usage should become eligible before the weekly reset, got %v", at)
	}
}

// The bundled late policy's weekly-only thresholds release leftover weekly
// quota on accounts with no five-hour window, and must not change anything
// for accounts that have one.
func TestWeeklyOnlyPaceThresholdsApplyOnlyWithoutShortWindow(t *testing.T) {
	late := func(s decision.UsageSnapshot) decision.Input {
		in := input(s)
		in.TriggerMargin, in.RollingReserve = .05, .40
		in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: 24 * time.Hour, MinWeeklyRemaining: .50}}
		in.WeeklyOnlyPaceThresholds = []decision.PaceThreshold{
			{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: .60},
			{TimeRemaining: 48 * time.Hour, MinWeeklyRemaining: .40},
			{TimeRemaining: 24 * time.Hour, MinWeeklyRemaining: .20},
			{TimeRemaining: 6 * time.Hour, MinWeeklyRemaining: .05},
		}
		return in
	}
	for _, tt := range []struct {
		name       string
		remaining  float64
		untilReset time.Duration
		want       decision.Decision
		wantReason string
	}{
		{"three days, 65% left", .65, 70 * time.Hour, decision.Admit, "weekly remaining meets weekly-only pace threshold"},
		{"three days, on pace", .45, 70 * time.Hour, decision.Wait, "no pace threshold matched"},
		{"two days, 45% left", .45, 40 * time.Hour, decision.Admit, "weekly remaining meets weekly-only pace threshold"},
		{"one day, 30% left", .30, 20 * time.Hour, decision.Admit, "weekly remaining meets weekly-only pace threshold"},
		{"one day, 60% left uses the base threshold", .60, 20 * time.Hour, decision.Admit, "weekly remaining meets pace threshold"},
		{"one day, 15% left", .15, 20 * time.Hour, decision.Wait, "no pace threshold matched"},
		{"final hours, 6% left", .06, 5 * time.Hour, decision.Admit, "weekly remaining meets weekly-only pace threshold"},
		{"five days out", .95, 120 * time.Hour, decision.Wait, "no pace threshold matched"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := limitedSnapshot()
			s.Short = nil
			s.Weekly = decision.UsageWindow{Remaining: tt.remaining, ResetsAt: now.Add(tt.untilReset)}
			got := decision.Evaluate(late(s))
			if got.Decision != tt.want || got.Reason != tt.wantReason || got.Mode != decision.ModePace {
				t.Fatalf("got %s/%s (%s), want %s (%s)", got.Decision, got.Mode, got.Reason, tt.want, tt.wantReason)
			}
		})
	}

	t.Run("short window ignores weekly-only thresholds", func(t *testing.T) {
		s := limitedSnapshot()
		s.Short = &decision.UsageWindow{Remaining: .90, ResetsAt: now.Add(2 * time.Hour)}
		s.Weekly = decision.UsageWindow{Remaining: .30, ResetsAt: now.Add(20 * time.Hour)}
		with := late(s)
		without := late(s)
		without.WeeklyOnlyPaceThresholds = nil
		got, base := decision.Evaluate(with), decision.Evaluate(without)
		if got.Decision != base.Decision || got.Reason != base.Reason || got.Mode != decision.ModeSlots {
			t.Fatalf("with=%s (%s) without=%s (%s)", got.Decision, got.Reason, base.Decision, base.Reason)
		}
		if got.Decision == decision.Admit {
			t.Fatalf("fixture should wait in slot mode so a leak would show: %#v", got)
		}
	})

	t.Run("projection reaches the first weekly-only row", func(t *testing.T) {
		s := limitedSnapshot()
		s.Short = nil
		s.Weekly = decision.UsageWindow{Remaining: .45, ResetsAt: now.Add(70 * time.Hour)}
		in := late(s)
		in.WeeklyOnlyPaceThresholds = in.WeeklyOnlyPaceThresholds[1:] // 48h/40% first
		at := decision.ProjectTriggerAt(in, time.Hour)
		if at == nil || !at.Equal(now.Add(22*time.Hour)) {
			t.Fatalf("projected = %v, want when 48h remain (%v)", at, now.Add(22*time.Hour))
		}
	})

	t.Run("weekly-only thresholds alone make a weekly-only decision known", func(t *testing.T) {
		s := limitedSnapshot()
		s.Short = nil
		in := input(s)
		in.WeeklyOnlyPaceThresholds = []decision.PaceThreshold{{TimeRemaining: 6 * time.Hour, MinWeeklyRemaining: .05}}
		if got := decision.Evaluate(in); got.Decision == decision.Unknown {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("invalid weekly-only threshold fails closed", func(t *testing.T) {
		s := limitedSnapshot()
		s.Short = nil
		in := input(s)
		in.WeeklyOnlyPaceThresholds = []decision.PaceThreshold{{TimeRemaining: 0, MinWeeklyRemaining: .05}}
		if got := decision.Evaluate(in); got.Decision != decision.Unknown {
			t.Fatalf("got %#v", got)
		}
	})
}

func TestEvaluateFailsClosedForStaleSnapshot(t *testing.T) {
	s := limitedSnapshot()
	s.ObservedAt = now.Add(-16 * time.Minute)
	got := decision.Evaluate(input(s))

	if got.Decision != decision.Unknown || got.Reason != "usage snapshot is stale" {
		t.Fatalf("got %q (%s)", got.Decision, got.Reason)
	}
}

func TestSnapshotExactlyAtMaximumAgeIsAccepted(t *testing.T) {
	s := limitedSnapshot()
	s.ObservedAt = now.Add(-15 * time.Minute)
	got := decision.Evaluate(input(s))

	if got.Decision == decision.Unknown {
		t.Fatalf("got %#v", got)
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

func TestProjectTriggerAtAcrossShortWindowReset(t *testing.T) {
	start := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	in := decision.Input{
		Snapshot: decision.UsageSnapshot{
			Provider:   "claude",
			ObservedAt: start,
			Short:      &decision.UsageWindow{Remaining: .75, ResetsAt: start.Add(2 * time.Hour)},
			Weekly:     decision.UsageWindow{Remaining: .40, ResetsAt: start.Add(21 * time.Hour)},
		},
		WindowWeeklyCost: .10,
		TriggerMargin:    .02,
		RollingReserve:   .25,
		Now:              start,
		MaxSnapshotAge:   15 * time.Minute,
	}

	got := decision.ProjectTriggerAt(in, 5*time.Minute)
	want := start.Add(2*time.Hour + 5*time.Minute)
	if got == nil || !got.Equal(want) {
		t.Fatalf("projected trigger = %v, want %v", got, want)
	}
}

func TestProjectTriggerAtUsesPaceThresholdBoundary(t *testing.T) {
	start := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	weeklyReset := start.Add(96 * time.Hour)
	in := decision.Input{
		Snapshot: decision.UsageSnapshot{
			Provider: "codex", ObservedAt: start,
			Weekly: decision.UsageWindow{Remaining: .80, ResetsAt: weeklyReset},
		},
		PaceThresholds:   []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: .50}},
		WindowWeeklyCost: .10, Now: start, MaxSnapshotAge: 15 * time.Minute,
	}

	got := decision.ProjectTriggerAt(in, 5*time.Minute)
	want := weeklyReset.Add(-72 * time.Hour)
	if got == nil || !got.Equal(want) {
		t.Fatalf("projected trigger = %v, want %v", got, want)
	}
}

func TestProjectTriggerAtUsesPaceGapBoundary(t *testing.T) {
	s := limitedSnapshot()
	s.Short = nil
	s.Weekly.Remaining = 0.90
	s.Weekly.ResetsAt = now.Add(110 * time.Hour)
	trigger := 0.30
	in := input(s)
	in.PaceThresholds = nil
	in.PaceGapTrigger = &trigger

	got := decision.ProjectTriggerAt(in, 5*time.Minute)
	if got == nil {
		t.Fatal("expected projected trigger")
	}
	want := now.Add(9*time.Hour + 15*time.Minute)
	if !got.Equal(want) {
		t.Fatalf("projected trigger = %s, want %s", got, want)
	}
}

func TestProjectTriggerAtReturnsNilWhenFlatUsageNeverQualifies(t *testing.T) {
	start := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	in := decision.Input{
		Snapshot: decision.UsageSnapshot{
			Provider: "codex", ObservedAt: start,
			Weekly: decision.UsageWindow{Remaining: .10, ResetsAt: start.Add(96 * time.Hour)},
		},
		PaceThresholds:   []decision.PaceThreshold{{TimeRemaining: 72 * time.Hour, MinWeeklyRemaining: .50}},
		WindowWeeklyCost: .10, Now: start, MaxSnapshotAge: 15 * time.Minute,
	}
	if got := decision.ProjectTriggerAt(in, 5*time.Minute); got != nil {
		t.Fatalf("projected trigger = %v, want nil", got)
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	const epsilon = 1e-9
	if got < want-epsilon || got > want+epsilon {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
