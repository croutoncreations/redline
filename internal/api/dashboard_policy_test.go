package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The phone draws where the scheduler will act on each meter, so the payload
// carries the effective policy's settings in the shape the bars need.
//
// The decision itself was already there; the thresholds behind it were not,
// so a phone could say "waiting" and not why, and had no way to show the
// quarter of the 5-hour window Redline never touches or the weekly floor it
// is waiting for. These are the settings for the provider's EFFECTIVE policy,
// which may be an override rather than the active default.
func TestDashboardCarriesTheSchedulingThresholdsThePhoneDraws(t *testing.T) {
	server, _ := newAPIServer(t, codexPayload)

	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Providers []struct {
			ID         string `json:"id"`
			Scheduling *struct {
				RollingReserve float64 `json:"rolling_reserve"`
				TriggerMargin  float64 `json:"trigger_margin"`
				PaceThresholds []struct {
					TimeRemainingSeconds int64   `json:"time_remaining_seconds"`
					MinWeeklyRemaining   float64 `json:"min_weekly_remaining"`
				} `json:"pace_thresholds"`
			} `json:"scheduling"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	var codex *struct {
		RollingReserve float64 `json:"rolling_reserve"`
		TriggerMargin  float64 `json:"trigger_margin"`
		PaceThresholds []struct {
			TimeRemainingSeconds int64   `json:"time_remaining_seconds"`
			MinWeeklyRemaining   float64 `json:"min_weekly_remaining"`
		} `json:"pace_thresholds"`
	}
	for _, p := range got.Providers {
		if p.ID == "codex-main" {
			codex = p.Scheduling
		}
	}
	if codex == nil {
		t.Fatal("codex-main has no scheduling block")
	}
	if codex.RollingReserve != 0.25 {
		t.Errorf("rolling_reserve = %v, want 0.25", codex.RollingReserve)
	}
	if codex.TriggerMargin != 0.02 {
		t.Errorf("trigger_margin = %v", codex.TriggerMargin)
	}
	// Durations travel as seconds. A Go duration string would have to be
	// parsed on two platforms; a number is read the same everywhere.
	if len(codex.PaceThresholds) != 1 || codex.PaceThresholds[0].TimeRemainingSeconds != 72*3600 || codex.PaceThresholds[0].MinWeeklyRemaining != 0.5 {
		t.Errorf("pace_thresholds = %+v, want one at 72h / 0.5", codex.PaceThresholds)
	}
}
