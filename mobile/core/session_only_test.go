package core

import "testing"

func TestSessionOnlyMetadata(t *testing.T) {
	view := renderPayload(t, `{"providers":[{"id":"team","provider":"claude","account_status":"ok","weekly_policy":"session_only","session_reserve":0.5,"scheduling":{"rolling_reserve":0.5},"snapshot":{"provider":"claude","observed_at":"2026-10-09T11:59:00Z","weekly_unavailable":true,"short_window_not_started":true}}]}`)
	seat := view.Providers[0]
	if seat.WeeklyPolicy != "session_only" || seat.SessionReserve != .5 || !seat.WeeklyNotReported || seat.Weekly != nil || seat.Scheduling.ReservePercent != 50 {
		t.Fatalf("seat=%+v", seat)
	}
}
