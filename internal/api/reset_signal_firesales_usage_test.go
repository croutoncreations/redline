package api

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

// Accounts are named in config (e.g. codex-main) while usage sources store
// snapshots under the provider type (codex). The baseline must use the latter.
func TestResetSignalFiresaleBaselineUsesConfiguredUsageProvider(t *testing.T) {
	s, _, _ := firesaleFixture(t, "auto")
	provider := s.config.Providers["main"]
	delete(s.config.Providers, "main")
	s.config.Providers["codex-main"] = provider
	at := s.now().Add(time.Second)
	s.now = func() time.Time { return at }
	snapshot := decision.UsageSnapshot{Provider: "codex", Source: "native", ObservedAt: at, Weekly: decision.UsageWindow{Remaining: .37, ResetsAt: at.Add(24 * time.Hour)}}
	if err := s.store.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.pollResetSignals(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	sales, err := s.store.ActiveResetSignalFiresales(t.Context())
	if err != nil || len(sales) != 1 || sales[0].ProviderAccountID != "codex-main" {
		t.Fatalf("sales=%+v err=%v", sales, err)
	}
	if sales[0].BaselineWeeklyRemaining == nil || *sales[0].BaselineWeeklyRemaining != .37 {
		t.Fatalf("baseline not captured from the codex snapshot: %v", sales[0].BaselineWeeklyRemaining)
	}
}

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
			// Snapshots are stored under the provider type ("codex"), as the
			// real usage sources do, while the sale belongs to account "main".
			s, signal, n := firesaleFixture(t, "auto")
			// The fixture already saved a codex snapshot at s.now(); start a
			// little later so this test's baseline is the latest one.
			start := s.now().Add(time.Second)
			s.now = func() time.Time { return start }
			usageProvider := "codex"
			if tc.noBaseline {
				// Store telemetry under another key so the sale starts with no
				// usage snapshot for its account.
				usageProvider = "unrelated"
			}
			baseline := decision.UsageSnapshot{Provider: usageProvider, Source: "native", ObservedAt: start, Weekly: decision.UsageWindow{Remaining: .4, ResetsAt: start.Add(24 * time.Hour)}}
			if err := s.store.SaveSnapshot(t.Context(), baseline, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.pollResetSignals(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			now := start.Add(2 * time.Minute)
			s.now = func() time.Time { return now }
			for i, remaining := range tc.remaining {
				post := baseline
				post.ObservedAt = start.Add(time.Duration(i+1) * time.Second)
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
