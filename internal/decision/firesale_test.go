package decision_test

import (
	"github.com/croutoncreations/redline/internal/domain"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

func TestEvaluateFiresaleOnlyUpgradesAdmittedDecisions(t *testing.T) {
	trigger := 0.35
	in := input(limitedSnapshot())
	in.FiresaleTrigger = &trigger
	got := decision.Evaluate(in)
	if !got.Firesale || got.UnlockedTier != domain.DispatchFiresale || !strings.Contains(got.Reason, "firesale") {
		t.Fatalf("got %+v", got)
	}
	in.Snapshot.Short.Remaining = in.RollingReserve
	got = decision.Evaluate(in)
	if got.Decision != decision.Wait || got.Firesale {
		t.Fatalf("reserve bypassed: %+v", got)
	}
	in.Snapshot.ObservedAt = now.Add(-time.Hour)
	got = decision.Evaluate(in)
	if got.Decision != decision.Unknown || got.Firesale {
		t.Fatalf("stale snapshot bypassed: %+v", got)
	}
	in = input(limitedSnapshot())
	got = decision.Evaluate(in)
	if got.Firesale || got.UnlockedTier != domain.DispatchExpiring {
		t.Fatalf("unset trigger changed behavior: %+v", got)
	}
	in.Snapshot.Short = nil
	in.PaceGapTrigger = &trigger
	in.FiresaleTrigger = &trigger
	got = decision.Evaluate(in)
	if !got.Firesale {
		t.Fatalf("pace-only firesale: %+v", got)
	}
	invalid := 1.1
	in.FiresaleTrigger = &invalid
	if got = decision.Evaluate(in); got.Decision != decision.Unknown {
		t.Fatalf("invalid trigger: %+v", got)
	}
}

func TestFiresaleUsesOverflowWhenHigherThanPaceGap(t *testing.T) {
	in := input(limitedSnapshot())
	in.Snapshot.Weekly.Remaining = 0.80
	in.Snapshot.Weekly.ResetsAt = now.Add(100 * time.Hour)
	in.WindowWeeklyCost = 0.01
	trigger := 0.45
	in.FiresaleTrigger = &trigger
	got := decision.Evaluate(in)
	if got.PaceGap >= trigger || got.Overflow < trigger || !got.Firesale {
		t.Fatalf("overflow firesale=%+v", got)
	}
}

func TestEffectiveHorizonMovesPaceThresholdAndBoundsProjection(t *testing.T) {
	in := input(limitedSnapshot())
	in.Snapshot.Short = nil
	in.Snapshot.Weekly.Remaining = 0.50
	in.Snapshot.Weekly.ResetsAt = now.Add(72 * time.Hour)
	in.PaceThresholds = []decision.PaceThreshold{{TimeRemaining: time.Hour, MinWeeklyRemaining: 0.50}}
	in.ResetEvents = []decision.ResetEvent{{Source: decision.ResetPredictedGlobal, At: now.Add(2 * time.Hour), Clears: []string{"weekly"}}}
	if got := decision.Evaluate(in); got.Decision != decision.Wait {
		t.Fatalf("initial=%+v", got)
	}
	projected := decision.ProjectTriggerAt(in, time.Hour)
	if projected == nil || !projected.Equal(now.Add(time.Hour)) {
		t.Fatalf("projection=%v", projected)
	}
	// No poll before the horizon matches; never project past the earlier reset.
	if got := decision.ProjectTriggerAt(in, 3*time.Hour); got != nil {
		t.Fatalf("projection crossed horizon: %v", got)
	}
}

func TestEvaluateEffectiveResetHorizon(t *testing.T) {
	in := input(limitedSnapshot())
	baseline := decision.Evaluate(in)
	earlier := now.Add(time.Hour)
	in.ResetEvents = []decision.ResetEvent{{Source: decision.ResetBankedReset, At: earlier, Clears: []string{"weekly"}}}
	got := decision.Evaluate(in)
	if got.EffectiveResetAt != earlier || got.EffectiveResetSource != decision.ResetBankedReset || got.PaceGap <= baseline.PaceGap || got.Overflow <= baseline.Overflow {
		t.Fatalf("horizon did not raise pressure: baseline=%+v got=%+v", baseline, got)
	}
	if len(got.Slots) != 1 || got.Slots[0].EndsAt != earlier {
		t.Fatalf("slots = %+v", got.Slots)
	}
	for _, event := range []decision.ResetEvent{
		{Source: decision.ResetBankedReset, At: earlier, Clears: []string{"five_hour"}},
		{Source: decision.ResetBankedReset, At: now, Clears: []string{"weekly"}},
		{Source: decision.ResetPredictedGlobal, At: in.Snapshot.Weekly.ResetsAt.Add(time.Hour), Clears: []string{"weekly"}},
	} {
		in.ResetEvents = []decision.ResetEvent{event}
		got = decision.Evaluate(in)
		if got.EffectiveResetAt != in.Snapshot.Weekly.ResetsAt || got.EffectiveResetSource != decision.ResetWeekly || got.PaceGap != baseline.PaceGap || got.Overflow != baseline.Overflow {
			t.Fatalf("ignored event changed result: %+v", got)
		}
	}
}
