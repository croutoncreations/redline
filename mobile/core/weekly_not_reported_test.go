package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// A Team seat (as the desktop sends it once cards without a weekly window
// are accepted): fresh 5-hour and Fable numbers, credits 0, weekly not
// reported. The phone must say "not reported" and never show a weekly
// number -- in particular not the 5-hour one.
func TestTeamSeatWeeklyIsNotReportedNotZero(t *testing.T) {
	view := renderPayload(t, `{"providers":[
	 {"id":"claude-5ea70003","provider":"claude","label":"Echo","account_status":"ok",
	  "snapshot":{"provider":"claude","observed_at":"2026-10-09T11:59:00Z","plan":"Team 5x","source":"openusage",
	   "weekly_unavailable":true,"banked_resets":0,
	   "short":{"remaining":0.76,"resets_at":"2026-10-09T14:30:00Z"},
	   "allowances":[{"key":"model:fable:weekly","source_label":"Fable","scope":"model","role":"weekly","remaining":1,
	    "resets_at":"2026-10-14T22:00:00Z","period_duration_seconds":604800}]}},
	 {"id":"claude-main","provider":"claude","account_status":"ok",
	  "snapshot":{"provider":"claude","observed_at":"2026-10-09T11:59:00Z","source":"openusage",
	   "weekly":{"remaining":0.45,"resets_at":"2026-10-15T17:00:00Z"}}}]}`)
	if len(view.Providers) != 2 {
		t.Fatalf("providers = %d", len(view.Providers))
	}
	seat := view.Providers[0]
	if !seat.WeeklyNotReported || seat.Weekly != nil {
		t.Fatalf("weekly = %+v notReported=%v", seat.Weekly, seat.WeeklyNotReported)
	}
	if seat.Session == nil || seat.Session.RemainingPercent != 76 {
		t.Fatalf("session = %+v", seat.Session)
	}
	if seat.BankedResets == nil || *seat.BankedResets != 0 || seat.Plan != "Team 5x" {
		t.Fatalf("credits=%v plan=%q", seat.BankedResets, seat.Plan)
	}
	encoded, _ := json.Marshal(seat)
	if !strings.Contains(string(encoded), `"model:fable:weekly"`) || strings.Contains(string(encoded), `"weekly":{`) {
		t.Fatalf("seat = %s", encoded)
	}
	if personal := view.Providers[1]; personal.WeeklyNotReported || personal.Weekly == nil || personal.Weekly.RemainingPercent != 45 {
		t.Fatalf("personal = %+v", personal.Weekly)
	}
}
