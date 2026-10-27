package api

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetSignalFiresaleUsageRefill(t *testing.T) {
	for _, tc := range []struct {
		name                                                                             string
		remaining                                                                        []float64
		advanceWindow, stale, beforeBaseline, sameObservation, noBaseline, otherProvider bool
		endReason, source                                                                string
	}{
		{name: "remaining rises", remaining: []float64{.6}, endReason: "completed", source: "usage_refill"},
		{name: "within epsilon", remaining: []float64{.41}},
		{name: "at epsilon", remaining: []float64{.42}},
		{name: "low water refill", remaining: []float64{.1, .3}, endReason: "completed", source: "usage_refill"},
		{name: "small recovery to start", remaining: []float64{.39, .4}},
		{name: "new window", remaining: []float64{.2}, advanceWindow: true, endReason: "completed", source: "usage_refill"},
		{name: "stale", remaining: []float64{.6}, stale: true},
		{name: "before baseline", remaining: []float64{.6}, beforeBaseline: true},
		{name: "same observation", remaining: []float64{.6}, sameObservation: true},
		{name: "no baseline", remaining: []float64{.6}, noBaseline: true},
		{name: "other provider", remaining: []float64{.6}, otherProvider: true},
		{name: "announcement takes precedence", remaining: []float64{.6}, endReason: "completed", source: "announcement"},
		{name: "disabled takes precedence", remaining: []float64{.6}, endReason: "disabled"},
		{name: "deadline takes precedence", remaining: []float64{.6}, endReason: "deadline_passed"},
		{name: "retraction takes precedence", remaining: []float64{.6}, endReason: "retracted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, signal, n := firesaleFixture(t, "auto")
			now := s.now()
			baseline := decision.UsageSnapshot{Provider: "main", ObservedAt: now.Add(-2 * time.Minute), Weekly: decision.UsageWindow{Remaining: .4, ResetsAt: now.Add(24 * time.Hour)}}
			if !tc.noBaseline {
				if err := s.store.SaveSnapshot(t.Context(), baseline, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			for i, remaining := range tc.remaining {
				post := baseline
				post.ObservedAt = now.Add(time.Duration(i)*time.Second - time.Minute)
				post.Weekly.Remaining = remaining
				if tc.advanceWindow {
					post.Weekly.ResetsAt = post.Weekly.ResetsAt.Add(7 * 24 * time.Hour)
				}
				if tc.stale {
					s.now = func() time.Time { return now.Add(30 * time.Minute) }
				}
				if tc.beforeBaseline {
					post.ObservedAt = baseline.ObservedAt.Add(-time.Second)
				}
				if tc.sameObservation {
					post.ObservedAt = baseline.ObservedAt
				}
				if tc.otherProvider {
					post.Provider = "other"
				}
				if err := s.store.SaveSnapshot(t.Context(), post, nil); err != nil {
					t.Fatal(err)
				}
				switch tc.name {
				case "announcement takes precedence":
					signal.Kind = resetsignal.KindCompleted
				case "disabled takes precedence":
					s.config.ResetSignals.Firesale = "off"
				case "deadline takes precedence":
					s.now = func() time.Time { return signal.Window.End.Add(time.Second) }
				case "retraction takes precedence":
					signal.Corrected = true
				}
				if err := s.pollResetSignals(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
				if len(tc.remaining) > 1 && i == 0 {
					sales, err := s.store.ActiveResetSignalFiresales(t.Context())
					if err != nil || len(sales) != 1 || sales[0].BaselineWeeklyRemaining == nil || *sales[0].BaselineWeeklyRemaining != remaining {
						t.Fatalf("low water=%+v err=%v", sales, err)
					}
					// The low-water mark must survive a server restart.
					s = newServer(s.config, s.store, s.now, nil, nil, n)
					installResetSignalFixture(t, s, signal)
				}
			}
			for range 2 {
				if err := s.pollResetSignals(t.Context(), ""); err != nil {
					t.Fatal(err)
				}
			}
			if tc.endReason == "" {
				if !s.ResetSignalState().Firesale.Active || len(n.events) != 1 {
					t.Fatalf("state=%+v events=%+v", s.ResetSignalState().Firesale, n.events)
				}
			} else {
				if s.ResetSignalState().Firesale.Active || len(n.events) != 2 {
					t.Fatalf("state=%+v events=%+v", s.ResetSignalState().Firesale, n.events)
				}
				end := n.events[1]
				if end.Type != domain.EventFiresaleEnded || end.Data["reason"] != tc.endReason || (tc.source != "" && end.Data["completion_source"] != tc.source) {
					t.Fatalf("end=%+v", end)
				}
				if tc.source == "usage_refill" && end.Message != "Your weekly allowance refilled, so the reset has landed. Back to normal pacing." {
					t.Fatalf("message=%q", end.Message)
				}
			}
		})
	}
}
