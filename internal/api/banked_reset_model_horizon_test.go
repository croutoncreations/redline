package api_test

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestBankedResetHorizonDoesNotShortenModelPool(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("")
	p := cfg.Providers["claude-main"]
	p.BankedResets = config.BankedResets{OnExpiry: "redeem"}
	cfg.Providers = map[string]config.Provider{"claude-main": p}
	policy := cfg.Policies["standard"]
	trigger := .45
	policy.FiresaleTrigger = &trigger
	cfg.Policies["standard"] = policy
	expires := apiNow.Add(3 * time.Hour)
	yes := true
	snapshot := decision.UsageSnapshot{Provider: "claude", Source: "test", ObservedAt: apiNow, Weekly: decision.UsageWindow{Remaining: .8, ResetsAt: apiNow.Add(6 * 24 * time.Hour)}, BankedResetDetails: []decision.BankedReset{{ID: "reset", ExpiresAt: &expires, ResetsLeft: 1, Claimable: true, UsableNow: &yes, Clears: []string{"weekly"}}}, Allowances: []decision.AllowanceWindow{{Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly", Remaining: .5, ResetsAt: apiNow.Add(6 * 24 * time.Hour), PeriodDurationSeconds: 604800}}}
	cfg.UsageMonitor.Enabled = true
	handler := api.NewHorizonTestServer(cfg, db, func() time.Time { return apiNow }, map[string]decision.UsageSnapshot{"claude-main": snapshot})
	server := httptest.NewServer(handler)
	defer server.Close()
	createClaudeCandidate(t, db, "profile", "fable", "fable", "job", 100)
	response := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
	if !response.Result.Firesale || len(response.Result.PoolResults) != 2 || response.Result.PoolResults[1].Pool != "model:fable:weekly" || response.Result.PoolResults[1].UnlockedTier == domain.DispatchFiresale {
		t.Fatalf("result=%+v", response.Result)
	}
}
