package decision_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
)

func TestAnnouncementFiresaleForcesPacingButProtectsShortWindow(t *testing.T) {
	in := input(limitedSnapshot())
	in.Snapshot.Weekly.Remaining = .1
	in.Snapshot.Weekly.ResetsAt = now.Add(7 * 24 * time.Hour)
	if got := decision.Evaluate(in); got.Decision != decision.Wait {
		t.Fatalf("baseline=%+v", got)
	}
	in.ForceFiresale = true
	got := decision.Evaluate(in)
	if got.Decision != decision.Admit || !got.Firesale || got.UnlockedTier != domain.DispatchFiresale {
		t.Fatalf("forced=%+v", got)
	}
	in.Snapshot.Short.Remaining = in.RollingReserve
	if got = decision.Evaluate(in); got.Decision != decision.Wait || got.Firesale {
		t.Fatalf("reserve bypassed=%+v", got)
	}
	in.Snapshot.Short = nil
	in.PaceThresholds = nil
	in.PaceGapTrigger = nil
	if got = decision.Evaluate(in); got.Decision != decision.Admit || !got.Firesale {
		t.Fatalf("pace-only=%+v", got)
	}
	// Forcing ignores pacing, not an empty allowance: nothing is left to spend.
	in.Snapshot.Weekly.Remaining = 0
	if got = decision.Evaluate(in); got.Decision == decision.Admit || got.Firesale {
		t.Fatalf("empty weekly allowance admitted=%+v", got)
	}
	in.Snapshot.Weekly.Remaining = .1
	in.Snapshot.ObservedAt = now.Add(-24 * time.Hour)
	if got = decision.Evaluate(in); got.Decision != decision.Unknown || got.Firesale {
		t.Fatalf("staleness bypassed=%+v", got)
	}
}
