package nativeusage

import (
	"fmt"
	"testing"
	"time"
)

func TestParseClaudeTeamWeeklyUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, weekly, limits string
		wantError            bool
		wantFable            bool
	}{
		{"team", `,"seven_day":null`, `,{"kind":"weekly_scoped","group":"weekly","percent":40,"resets_at":"2026-10-12T22:00:00Z","scope":{"model":{"display_name":"Fable"}}}`, false, true},
		{"missing fable reset", `,"seven_day":null`, `,{"kind":"weekly_scoped","group":"weekly","percent":40,"scope":{"model":{"display_name":"Fable"}}}`, false, false},
		{"missing weekly", ``, ``, true, false},
		{"malformed weekly", `,"seven_day":12`, ``, true, false},
		{"malformed weekly object", `,"seven_day":{}`, ``, true, false},
		{"weekly all", `,"seven_day":null`, `,{"kind":"weekly_all","percent":20,"resets_at":"2026-10-12T22:00:00Z"}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"five_hour":{"utilization":12,"resets_at":"2026-10-11T01:00:00Z"}%s,"limits":[{"kind":"session","group":"session","percent":12,"resets_at":"2026-10-11T01:00:00Z"}%s]}`, tc.weekly, tc.limits)
			got, err := parseClaude([]byte(body), now)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v snapshot=%+v", err, got)
			}
			if err != nil {
				return
			}
			if !got.WeeklyUnavailable || got.Short == nil || got.Short.Remaining != .88 {
				t.Fatalf("snapshot=%+v", got)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, ok := got.Allowance("weekly"); ok {
				t.Fatal("weekly invented")
			}
			fable, ok := got.Allowance("model:fable:weekly")
			if ok != tc.wantFable {
				t.Fatalf("fable=%+v found=%v", fable, ok)
			}
			if ok && (fable.Remaining != .6 || !fable.ResetsAt.Equal(now.Add(48*time.Hour)) || fable.ResetInferred) {
				t.Fatalf("fable=%+v", fable)
			}
		})
	}
}
