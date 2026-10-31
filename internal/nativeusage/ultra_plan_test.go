package nativeusage_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
)

// The first publicly reported real response from a verified Ultra account
// (https://github.com/cortexkit/anthropic-auth/issues/572, 2026-10-30): HTTP
// 200 with "five_hour": null, a seven_day window carrying extra dollar fields,
// and a limits array with weekly_all/weekly_scoped entries but no session.
func TestClaudeNativeParsesObservedVerifiedUltraResponse(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
  "five_hour": null,
  "seven_day": {"utilization": 0, "resets_at": "2026-11-06T20:59:59.701522+00:00",
    "limit_dollars": null, "used_dollars": null, "remaining_dollars": null, "locked_reason": null},
  "limits": [
    {"kind": "weekly_all", "percent": 0, "resets_at": "2026-11-06T20:59:59.701522+00:00"},
    {"kind": "weekly_scoped", "percent": 0, "resets_at": "2026-11-06T20:59:59.701522+00:00", "scope": {"model": {"display_name": "Fable"}}}
  ]
}`)
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "t"},
		ClaudeUsageURL: server.URL, Now: func() time.Time { return now }}
	got, _, err := client.Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Short != nil || got.Weekly.Remaining != 1 || !got.Weekly.ResetsAt.Equal(time.Date(2026, 11, 6, 20, 59, 59, 701522000, time.UTC)) {
		t.Fatalf("snapshot = %#v", got)
	}
	if _, ok := got.Allowance("session"); ok {
		t.Fatal("no session allowance expected")
	}
	if fable, ok := got.Allowance("model:fable:weekly"); !ok || fable.Remaining != 1 {
		t.Fatalf("fable = %#v ok=%v", fable, ok)
	}
}

// Since 2026-10-30, verified Ultra (formerly Max 20x) accounts have no 5-hour
// limit; Max (formerly Max 5x), Pro, and unverified Ultra still have one.
// Scheduling must follow the windows the API reports, never the plan name.
func TestClaudeNativeSchedulesFromReportedWindowsNotPlan(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	weekly := `"seven_day":{"utilization":20,"resets_at":"2026-11-02T12:00:00Z"}`
	for _, tt := range []struct {
		name, fiveHour string
		wantShort      bool
		wantMode       decision.Mode
	}{
		{"verified ultra null session", `"five_hour":{"utilization":null,"resets_at":null},`, false, decision.ModePace},
		{"verified ultra null window", `"five_hour":null,`, false, decision.ModePace},
		{"verified ultra omitted window", ``, false, decision.ModePace},
		{"unverified ultra or max session", `"five_hour":{"utilization":40,"resets_at":"2026-10-31T14:00:00Z"},`, true, decision.ModeSlots},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{%s%s}`, tt.fiveHour, weekly)
			}))
			defer server.Close()
			client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "t"},
				ClaudeUsageURL: server.URL, Now: func() time.Time { return now }}
			snapshot, _, err := client.Fetch(context.Background(), config.Provider{Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			if (snapshot.Short != nil) != tt.wantShort {
				t.Fatalf("short = %#v, want present=%v", snapshot.Short, tt.wantShort)
			}
			if _, ok := snapshot.Allowance("session"); ok != tt.wantShort {
				t.Fatalf("session allowance present=%v, want %v", ok, tt.wantShort)
			}

			// 80% of the week left with two days to go: well behind pace.
			gap := .30
			in := decision.Input{Snapshot: snapshot, WindowWeeklyCost: .08, TriggerMargin: .02, RollingReserve: .25,
				PaceGapTrigger: &gap, Now: now, MaxSnapshotAge: 15 * time.Minute}
			got := decision.Evaluate(in)
			if got.Decision != decision.Admit || got.Mode != tt.wantMode {
				t.Fatalf("decision = %s/%s (%s), want RUN/%s", got.Decision, got.Mode, got.Reason, tt.wantMode)
			}
			if at := decision.ProjectTriggerAt(in, 5*time.Minute); at == nil || !at.Equal(now) {
				t.Fatalf("projected trigger = %v, want now", at)
			}
			if !tt.wantShort && (len(got.Slots) != 0 || got.RollingDispatchable != 0) {
				t.Fatalf("weekly-only account must not be budgeted on 5-hour slots: %#v", got)
			}
		})
	}
}
