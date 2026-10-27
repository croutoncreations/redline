package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func claudeTeamAllowancePayload(short, fable float64) string {
	return fmt.Sprintf(`{
  "schema":"openusage.limits.v1",
  "providers":{"claude":{"fetchedAt":%q,"plan":"Team 5x","resources":{
    "session":{"kind":"consumption","limit":100,"remaining":%f,"used":%f,"utilization":%f,"unit":"percent","windowSeconds":18000,"resetsAt":%q},
    "fable":{"kind":"consumption","limit":100,"remaining":%f,"used":%f,"utilization":%f,"unit":"percent","windowSeconds":604800,"resetsAt":%q}
  }}}
}`, apiNow.Format(time.RFC3339), short*100, (1-short)*100, 1-short, apiNow.Add(time.Hour).Format(time.RFC3339), fable*100, (1-fable)*100, 1-fable, apiNow.Add(time.Hour).Format(time.RFC3339))
}

func TestSessionOnlyExecutesBehindTask(t *testing.T) {
	for _, weeklyPolicy := range []string{"", "session_only"} {
		t.Run("policy="+weeklyPolicy, func(t *testing.T) {
			usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, claudeTeamAllowancePayload(.8, .9))
			}))
			defer usage.Close()
			db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig(usage.URL)
			p := cfg.Providers["claude-main"]
			p.WeeklyPolicy, p.OpenUsageCard = weeklyPolicy, "claude"
			cfg.Providers["claude-main"] = p
			createClaudeCandidate(t, db, "profile", "opus", "", "task", 100)
			executed := make(chan struct{}, 1)
			handler := api.NewServerWithDependencies(cfg, db, func() time.Time { return apiNow }, fakeExecutor{
				execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error {
					executed <- struct{}{}
					return nil
				},
			}, fakeRevisionResolver{})
			server := httptest.NewServer(handler)
			defer server.Close()
			result := postJSON[schedulerResponseForTest](t, server.URL+"/v1/scheduler/execute", map[string]any{"provider_account_id": "claude-main"})
			if weeklyPolicy == "" {
				if result.Run != nil || result.Result.Decision != decision.Unknown {
					t.Fatalf("result=%+v", result)
				}
				return
			}
			if result.Run == nil || result.Run.TaskID != "task" || result.Result.UnlockedTier != domain.DispatchBehind {
				t.Fatalf("result=%+v", result)
			}
			select {
			case <-executed:
			case <-time.After(time.Second):
				t.Fatal("executor not called")
			}
		})
	}
}

func TestSessionOnlyCandidateBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, policy, model string
		short, fable        float64
		selected            bool
	}{
		{"opted in behind", "session_only", "opus", .8, .9, true},
		{"default held", "", "opus", .8, .9, false},
		{"fable available", "session_only", "fable", .8, .9, true},
		{"fable exhausted", "session_only", "fable", .8, 0, false},
		{"model pressure cannot spend reserve", "session_only", "fable", .5, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := claudeTeamAllowancePayload(tc.short, tc.fable)
			server, db := newAPIServerConfigured(t, payload, func(cfg *config.Config) {
				p := cfg.Providers["claude-main"]
				p.WeeklyPolicy = tc.policy
				p.OpenUsageCard = "claude"
				cfg.Providers["claude-main"] = p
			})
			createClaudeCandidate(t, db, "profile", tc.model, "", "task", 100)
			result := postJSON[struct {
				Result       decision.Result `json:"result"`
				SelectedTask *domain.Task    `json:"selected_task"`
			}](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "claude-main"})
			if (result.SelectedTask != nil) != tc.selected {
				t.Fatalf("selected=%+v result=%+v", result.SelectedTask, result.Result)
			}
			if tc.selected {
				if result.Result.Mode != decision.ModeSessionOnly || result.Result.UnlockedTier != domain.DispatchBehind || len(result.Result.PoolResults) == 0 || result.Result.PoolResults[0].Pool != "session" || result.Result.PoolResults[0].Remaining != tc.short {
					t.Fatalf("result=%+v", result.Result)
				}
				for _, pool := range result.Result.RequiredPools {
					if pool == "weekly" {
						t.Fatal("invented weekly pool")
					}
				}
			}
			if tc.name == "fable exhausted" && (len(result.Result.CandidateRejections) == 0 || !strings.Contains(result.Result.CandidateRejections[0].Reason, "model:fable:weekly allowance is exhausted")) {
				t.Fatalf("result=%+v", result.Result)
			}
			resp, err := http.Get(server.URL + "/v1/dashboard")
			if err != nil {
				t.Fatal(err)
			}
			var dashboard struct {
				Providers []struct {
					ID             string  `json:"id"`
					WeeklyPolicy   string  `json:"weekly_policy"`
					SessionReserve float64 `json:"session_reserve"`
				} `json:"providers"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&dashboard); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			for _, p := range dashboard.Providers {
				if p.ID == "claude-main" && (p.WeeklyPolicy != tc.policy || (tc.policy != "" && p.SessionReserve != .5)) {
					t.Fatalf("dashboard=%+v", p)
				}
			}
			resp, err = http.Get(server.URL + "/v1/firesales")
			if err != nil {
				t.Fatal(err)
			}
			var sales struct {
				Eligible []struct {
					Account string  `json:"account"`
					Reason  *string `json:"reason_unavailable"`
				} `json:"eligible"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&sales); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			for _, p := range sales.Eligible {
				if p.Account == "claude-main" && ((p.Reason == nil) != (tc.policy != "")) {
					t.Fatalf("firesales=%+v", p)
				}
			}
			// Manual firesales remain subject to the hidden-weekly policy opt-in.
			request := `{"account":"claude-main","until":"15m"}`
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/firesales", strings.NewReader(request))
			req.Header.Set("Content-Type", "application/json")
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			want := http.StatusCreated
			if tc.policy == "" {
				want = http.StatusConflict
			}
			if resp.StatusCode != want {
				t.Fatalf("firesale status=%d want=%d", resp.StatusCode, want)
			}
		})
	}
}
