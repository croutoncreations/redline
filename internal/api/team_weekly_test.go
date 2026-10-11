package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/store"
)

// The synthetic Team-seat OpenUsage answer (shared with the parser tests):
// Team seats without a weekly window must show their 5-hour,
// Fable and credit numbers, weekly "not reported", and hold dispatch.
func TestTeamSeatsWithoutAWeeklyWindowShowUsageAndHoldDispatch(t *testing.T) {
	fixture, err := os.ReadFile("../openusage/testdata/limits-claude-team-no-weekly.json")
	if err != nil {
		t.Fatal(err)
	}
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/limits/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.MaxSnapshotAge = "8760h" // the fixture's fetchedAt is fixed in time
	cfg.Providers = map[string]config.Provider{
		"claude-main": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude", WindowWeeklyCost: 0.08},
		"claude-5ea70003": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude@a0000004",
			Label: "Echo", WindowWeeklyCost: 0.08},
		"claude-5ea70001": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude@a0000002",
			Label: "Delta", WindowWeeklyCost: 0.08},
	}
	now := time.Date(2026, 9, 10, 1, 20, 0, 0, time.UTC)
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return now }))
	defer server.Close()
	for id := range cfg.Providers {
		postJSON[map[string]any](t, server.URL+"/v1/providers/"+id+"/refresh", map[string]any{})
	}
	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var dashboard struct {
		Providers []struct {
			ID            string                     `json:"id"`
			AccountStatus string                     `json:"account_status"`
			Snapshot      map[string]json.RawMessage `json:"snapshot"`
			Latest        *struct {
				Decision string `json:"decision"`
				Reason   string `json:"reason"`
			} `json:"latest_decision"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range dashboard.Providers {
		switch row.ID {
		case "claude-5ea70003", "claude-5ea70001":
			seen++
			if row.AccountStatus != "ok" || row.Snapshot == nil {
				t.Fatalf("%s: status=%s snapshot=%v", row.ID, row.AccountStatus, row.Snapshot != nil)
			}
			if _, has := row.Snapshot["weekly"]; has || string(row.Snapshot["weekly_unavailable"]) != "true" {
				t.Fatalf("%s: weekly must be absent and marked not reported: %v", row.ID, row.Snapshot)
			}
			if !strings.Contains(string(row.Snapshot["allowances"]), `"model:fable:weekly"`) ||
				string(row.Snapshot["banked_resets"]) != "0" || string(row.Snapshot["plan"]) != `"Team 5x"` {
				t.Fatalf("%s: snapshot = %v", row.ID, row.Snapshot)
			}
			if row.ID == "claude-5ea70003" && !strings.Contains(string(row.Snapshot["short"]), `"remaining":0.7`) {
				t.Fatalf("Echo's running 5-hour window was lost: %s", row.Snapshot["short"])
			}
			if row.ID == "claude-5ea70001" && string(row.Snapshot["short_window_not_started"]) != "true" {
				t.Fatalf("Delta's idle 5-hour window: %v", row.Snapshot)
			}
		case "claude-main":
			if _, has := row.Snapshot["weekly"]; !has || row.AccountStatus != "ok" {
				t.Fatalf("the personal account lost its weekly window: %v", row.Snapshot)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("team rows missing: %s", raw)
	}
	// Dispatch for a seat without a weekly window holds with a plain reason.
	result := postJSON[struct {
		Result struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		} `json:"result"`
	}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-5ea70003"})
	if result.Result.Decision != "UNKNOWN" || !strings.Contains(result.Result.Reason, "weekly limit not reported") {
		t.Fatalf("decision = %+v", result.Result)
	}
}
