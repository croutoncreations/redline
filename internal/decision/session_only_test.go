package decision

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

func TestSessionOnlyDoesNotChangeReportedWeekly(t *testing.T) {
	now := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)
	in := Input{Snapshot: UsageSnapshot{Provider: "claude", ObservedAt: now,
		Weekly: UsageWindow{Remaining: .9, ResetsAt: now.Add(23 * time.Hour)},
		Short:  &UsageWindow{Remaining: .8, ResetsAt: now.Add(time.Hour)}},
		WindowWeeklyCost: .08, RollingReserve: .25, Now: now, MaxSnapshotAge: 15 * time.Minute}
	want := Evaluate(in)
	in.SessionOnly, in.SessionReserve = true, .9
	if got := Evaluate(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestSessionOnly(t *testing.T) {
	now := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		change   func(*Input)
		want     Decision
		tier     domain.DispatchTier
		reason   string
		firesale bool
	}{
		{name: "room", want: Admit, tier: domain.DispatchBehind, reason: "session-only: 5-hour window has room above the reserve; weekly limit not reported"},
		{name: "reserve", change: func(in *Input) { in.Snapshot.Short.Remaining = .5 }, want: Wait, reason: "5-hour reserve protected (weekly limit not reported; session-only policy)"},
		{name: "below reserve", change: func(in *Input) { in.Snapshot.Short.Remaining = .2 }, want: Wait},
		{name: "not started", change: func(in *Input) { in.Snapshot.Short = nil; in.Snapshot.ShortWindowNotStarted = true }, want: Admit, tier: domain.DispatchBehind},
		{name: "stale", change: func(in *Input) { in.Snapshot.ObservedAt = now.Add(-time.Hour) }, want: Unknown, reason: "usage snapshot is stale"},
		{name: "source stale", change: func(in *Input) { in.Snapshot.SourceStale = true }, want: Unknown},
		{name: "future", change: func(in *Input) { in.Snapshot.ObservedAt = now.Add(time.Minute) }, want: Unknown},
		{name: "unreadable short", change: func(in *Input) { in.Snapshot.Short = nil; in.Snapshot.ShortWindowUnavailable = true }, want: Unknown},
		{name: "expired short", change: func(in *Input) { in.Snapshot.Short.ResetsAt = now }, want: Unknown},
		{name: "manual", change: func(in *Input) { in.ForceFiresale = true; in.ForceFiresaleTrigger = "manual" }, want: Admit, tier: domain.DispatchFiresale, firesale: true, reason: "manual firesale; running queued work before the deadline"},
		{name: "announcement", change: func(in *Input) { in.ForceFiresale = true }, want: Admit, tier: domain.DispatchFiresale, firesale: true, reason: "reset announcement firesale; running queued work before the deadline"},
		{name: "forced reserve", change: func(in *Input) { in.ForceFiresale = true; in.Snapshot.Short.Remaining = .5 }, want: Wait},
		{name: "default holds", change: func(in *Input) { in.SessionOnly = false }, want: Unknown, reason: "weekly limit not reported; dispatch holds until a policy exists for this account type"},
		{name: "invalid reserve", change: func(in *Input) { in.SessionReserve = 1.1 }, want: Unknown},
		{name: "nan reserve", change: func(in *Input) { in.SessionReserve = math.NaN() }, want: Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := teamSnapshot(now)
			snapshot.ShortWindowNotStarted = false
			snapshot.Short = &UsageWindow{Remaining: .8, ResetsAt: now.Add(time.Hour)}
			zero := 0.0
			in := Input{Snapshot: snapshot, SessionOnly: true, SessionReserve: .5, WindowWeeklyCost: .08, Now: now, MaxSnapshotAge: 15 * time.Minute, FiresaleTrigger: &zero}
			if tc.change != nil {
				tc.change(&in)
			}
			got := Evaluate(in)
			if got.Decision != tc.want || got.UnlockedTier != tc.tier || got.Firesale != tc.firesale || (tc.reason != "" && got.Reason != tc.reason) {
				t.Fatalf("got %+v", got)
			}
			if got.PaceGap != 0 || got.Overflow != 0 {
				t.Fatalf("weekly pressure invented: %+v", got)
			}
			if got.Decision == Admit {
				remaining := 1.0
				if in.Snapshot.Short != nil {
					remaining = in.Snapshot.Short.Remaining
					if !got.EffectiveResetAt.Equal(in.Snapshot.Short.ResetsAt) {
						t.Fatal("wrong reset")
					}
				} else if !got.EffectiveResetAt.IsZero() {
					t.Fatal("invented reset")
				}
				if math.Abs(got.RollingDispatchable-(remaining-in.SessionReserve)) > 1e-9 || got.Mode != ModeSessionOnly || got.EffectiveResetSource != ResetSession {
					t.Fatalf("got %+v", got)
				}
			}
		})
	}
}
