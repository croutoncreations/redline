package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

func TestBankedResetHorizon(t *testing.T) {
	for _, tc := range []struct {
		name, mode, provider    string
		expires                 time.Duration
		requiresLimit, consumed bool
		wantBanked              bool
	}{
		{"redeem", "redeem", "claude", 3 * time.Hour, false, false, true},
		{"codex", "redeem", "codex", 3 * time.Hour, false, false, true},
		{"notify", "notify", "claude", 3 * time.Hour, false, false, false},
		{"off", "off", "claude", 3 * time.Hour, false, false, false},
		{"after weekly", "redeem", "claude", 200 * time.Hour, false, false, false},
		{"requires limit", "redeem", "claude", 3 * time.Hour, true, false, false},
		{"unverified consumed", "redeem", "codex", 3 * time.Hour, false, true, false},
		{"five hour only", "redeem", "claude", 3 * time.Hour, false, false, false},
		{"not claimable", "redeem", "claude", 3 * time.Hour, false, false, false},
		{"unknown usability", "redeem", "claude", 3 * time.Hour, false, false, false},
		{"cooldown after action", "redeem", "claude", 3 * time.Hour, false, false, false},
		{"no credits", "redeem", "codex", 3 * time.Hour, false, false, false},
		{"action due", "redeem", "claude", time.Hour, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig("")
			id := tc.provider + "-main"
			p := cfg.Providers[id]
			p.BankedResets = config.BankedResets{OnExpiry: tc.mode}
			cfg.Providers = map[string]config.Provider{id: p}
			policy := cfg.Policies["standard"]
			trigger := .45
			policy.FiresaleTrigger = &trigger
			cfg.Policies["standard"] = policy
			expires := apiNow.Add(tc.expires)
			yes := true
			snapshot := decision.UsageSnapshot{Provider: tc.provider, ObservedAt: apiNow, Source: "test", Weekly: decision.UsageWindow{Remaining: .8, ResetsAt: apiNow.Add(6 * 24 * time.Hour)}, BankedResetDetails: []decision.BankedReset{{ID: "reset", ExpiresAt: &expires, ResetsLeft: 1, Claimable: true, UsableNow: &yes, RequiresLimit: &tc.requiresLimit, Clears: []string{"weekly"}}}}
			reset := &snapshot.BankedResetDetails[0]
			switch tc.name {
			case "five hour only":
				reset.Clears = []string{"five_hour"}
			case "not claimable":
				reset.Claimable = false
			case "unknown usability":
				reset.UsableNow = nil
			case "cooldown after action":
				reset.CooldownUntil = &expires
			case "no credits":
				reset.ResetsLeft = 0
			}
			// An acknowledged redemption without verified telemetry must not leave a future horizon.
			if tc.consumed {
				a, err := db.BeginBankedResetRedemption(t.Context(), store.BankedResetRedemption{ProviderAccountID: id, ResetID: "reset", RequestID: "key", Trigger: "automatic", CreatedAt: apiNow})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.CompleteBankedResetRedemption(t.Context(), a.ID, "redeemed", "reset", "unverified", apiNow); err != nil {
					t.Fatal(err)
				}
			}
			cfg.UsageMonitor.Enabled = true
			handler := api.NewHorizonTestServer(cfg, db, func() time.Time { return apiNow }, map[string]decision.UsageSnapshot{id: snapshot})
			server := httptest.NewServer(handler)
			defer server.Close()
			response, err := server.Client().Post(server.URL+"/v1/providers/"+id+"/decision", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var got struct {
				Result decision.Result `json:"result"`
			}
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			want := snapshot.Weekly.ResetsAt
			if tc.wantBanked {
				want = expires.Add(-time.Hour)
			}
			if response.StatusCode != 200 || !got.Result.EffectiveResetAt.Equal(want) || got.Result.Firesale != tc.wantBanked {
				t.Fatalf("status=%d result=%+v want horizon=%s firesale=%v", response.StatusCode, got.Result, want, tc.wantBanked)
			}
		})
	}
}
