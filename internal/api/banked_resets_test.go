package api_test

import (
	"encoding/json"
	"testing"

	"github.com/croutoncreations/redline/internal/decision"
)

func TestBankedResetDetailsAppearInStatusAndDashboard(t *testing.T) {
	t.Parallel()
	server, db := newAPIServer(t, codexPayload)
	snapshot := decision.UsageSnapshot{Account: "codex-main", Provider: "codex", ObservedAt: apiNow,
		Weekly: decision.UsageWindow{Remaining: .5, ResetsAt: apiNow.AddDate(0, 0, 4)}, Source: "test"}
	snapshot.ApplyBankedResets(&decision.BankedResets{Available: 1,
		Details: []decision.BankedReset{{ID: "credit", ResetsLeft: 1, Clears: []string{"five_hour", "weekly"}}}})
	if err := db.SaveSnapshot(t.Context(), snapshot, nil); err != nil {
		t.Fatal(err)
	}
	var status map[string]json.RawMessage
	getJSON(t, server.URL+"/v1/providers/codex-main/status", &status)
	var dashboard struct {
		Providers []struct {
			ID       string                     `json:"id"`
			Snapshot map[string]json.RawMessage `json:"snapshot"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	var dashboardSnapshot map[string]json.RawMessage
	for _, provider := range dashboard.Providers {
		if provider.ID == "codex-main" {
			dashboardSnapshot = provider.Snapshot
		}
	}
	for name, got := range map[string]map[string]json.RawMessage{"status": status, "dashboard": dashboardSnapshot} {
		var details []decision.BankedReset
		if err := json.Unmarshal(got["banked_reset_details"], &details); err != nil {
			t.Fatalf("%s: missing banked_reset_details: %v", name, err)
		}
		if len(details) != 1 || details[0].ID != "credit" || details[0].ResetsLeft != 1 {
			t.Fatalf("%s details = %#v", name, details)
		}
	}
}
