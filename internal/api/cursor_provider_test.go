package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/cursorusage"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

// cursorLimitsCard is an openusage.limits.v1 Cursor card at apiNow. Pools are
// percent used; the month resets in resetIn. On-demand and Grok Bot are
// present so the test can prove they never become scheduling headroom.
func cursorLimitsCard(cursorUsed, otherUsed, totalUsed float64, resetIn time.Duration) string {
	reset := apiNow.Add(resetIn).Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"schema":"openusage.limits.v1","generatedAt":%q,"errors":[],"providers":{"cursor":{
  "displayName":"Cursor","plan":"Pro","fetchedAt":%q,"expiresAt":%q,"stale":false,"resources":{
    "autoUsage":{"kind":"consumption","unit":"percent","used":%f,"limit":100,"resetsAt":%q,"windowSeconds":2592000},
    "apiUsage":{"kind":"consumption","unit":"percent","used":%f,"limit":100,"resetsAt":%q,"windowSeconds":2592000},
    "totalUsage":{"kind":"consumption","unit":"percent","used":%f,"limit":100,"resetsAt":%q,"windowSeconds":2592000},
    "onDemand":{"kind":"consumption","unit":"usd","used":1,"limit":500,"resetsAt":%q},
    "grokBot":{"kind":"consumption","unit":"percent","used":1,"limit":100,"resetsAt":%q,"windowSeconds":604800},
    "credits":{"kind":"balance","unit":"usd","available":50}}}}}`,
		apiNow.Format(time.RFC3339Nano), apiNow.Add(-time.Minute).Format(time.RFC3339Nano),
		apiNow.Add(4*time.Minute).Format(time.RFC3339Nano),
		cursorUsed, reset, otherUsed, reset, totalUsed, reset, reset, reset)
}

func newCursorServer(t *testing.T, card string) (*httptest.Server, *store.DB) {
	t.Helper()
	openUsage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/limits/cursor" {
			// Claude/Codex usage is not part of these tests.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, card)
	}))
	t.Cleanup(openUsage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Providers["cursor-main"] = config.Provider{
		Provider: "cursor", UsageSource: "openusage", OpenUsageURL: openUsage.URL, WindowWeeklyCost: 0.10,
	}
	trigger := 0.10
	policy := cfg.Policies["standard"]
	policy.PaceGapTrigger = &trigger
	cfg.Policies["standard"] = policy
	apiServer := api.NewServer(cfg, db, func() time.Time { return apiNow })
	// This machine's Cursor sign-ins are not part of these tests.
	apiServer.SetCursorAccountCheck(sameAccount{})
	server := httptest.NewServer(apiServer)
	t.Cleanup(server.Close)
	return server, db
}

type sameAccount struct{}

func (sameAccount) Verify(context.Context) error { return nil }

func createCursorCandidate(t *testing.T, db *store.DB, profileID, model, taskID string, priority int) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: profileID, ProviderAccountID: "cursor-main", HarnessType: "cursor-agent",
		Model: model, WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: taskID, Name: taskID, Priority: priority, ExecutionProfileID: profileID, Type: domain.OneOff,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
}

type cursorEvaluation struct {
	Result       decision.Result `json:"result"`
	SelectedTask *domain.Task    `json:"selected_task,omitempty"`
}

// With Other Models used up and Cursor's own pool barely touched halfway
// through the month, an Auto task runs and an Opus task (Other Models) does
// not, even though it has higher priority.
func TestCursorSchedulesFromThePoolTheModelDrawsFrom(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(5, 100, 40, 15*24*time.Hour))
	createCursorCandidate(t, db, "cursor-opus", "claude-opus-5-5-high", "opus-task", 100)
	createCursorCandidate(t, db, "cursor-auto", "auto", "auto-task", 50)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask == nil || result.SelectedTask.ID != "auto-task" {
		t.Fatalf("selected=%#v result=%#v", result.SelectedTask, result.Result)
	}
	if strings.Join(result.Result.RequiredPools, ",") != "weekly,model:cursor_models:monthly" {
		t.Fatalf("required pools = %v", result.Result.RequiredPools)
	}
	rejected := false
	for _, rejection := range result.Result.CandidateRejections {
		if rejection.TaskID == "opus-task" && strings.Contains(rejection.Reason, "model:other_models:monthly allowance is exhausted") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("opus task was not rejected for its exhausted pool: %#v", result.Result.CandidateRejections)
	}
}

// Pace is judged against the month, not a week: 60% left with half the month
// to go is only slightly ahead, so nothing runs. The same numbers against a
// 7-day period would look far behind pace and release work.
func TestCursorPaceUsesTheMonthlyPeriod(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(40, 40, 40, 15*24*time.Hour))
	createCursorCandidate(t, db, "cursor-auto", "auto", "auto-task", 50)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask != nil {
		t.Fatalf("selected %#v with pace gap %.2f", result.SelectedTask, result.Result.PaceGap)
	}
	if result.Result.PaceGap < 0.05 || result.Result.PaceGap > 0.15 {
		t.Fatalf("pace gap = %.3f, want ~0.10 (0.60 left - 0.50 of the month left)", result.Result.PaceGap)
	}
}

// Both pools used up: on-demand spend, credits and Grok Bot are all
// available in the card, and none of it may release work.
func TestCursorNeverSchedulesOnOnDemandOrGrokBot(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(100, 100, 100, 3*24*time.Hour))
	createCursorCandidate(t, db, "cursor-auto", "auto", "auto-task", 50)
	createCursorCandidate(t, db, "cursor-gpt", "gpt-5.6-sol-high", "gpt-task", 60)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask != nil || result.Result.Decision == decision.Admit {
		t.Fatalf("selected=%#v result=%#v", result.SelectedTask, result.Result)
	}
}

// Every Cursor model resolves to a pool; none is "account only".
func TestCursorModelRoutingIsAlwaysAPool(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(5, 5, 5, 3*24*time.Hour))
	createCursorCandidate(t, db, "cursor-composer", "composer-2.5", "composer-task", 50)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask == nil || result.Result.ModelRouting != "cursor_pool" ||
		!strings.Contains(strings.Join(result.Result.RequiredPools, ","), "model:cursor_models:monthly") {
		t.Fatalf("selected=%#v result=%#v", result.SelectedTask, result.Result)
	}
}

// The dashboard serves a Cursor account with a monthly period and its pools,
// and the usage monitor path (fetch, store, reload) keeps the period.
func TestCursorDashboardCarriesMonthlyPeriodAfterReload(t *testing.T) {
	server, _ := newCursorServer(t, cursorLimitsCard(5, 100, 40, 15*24*time.Hour))
	_ = postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})

	resp, err := http.Get(server.URL + "/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dashboard struct {
		Providers []struct {
			ID       string                  `json:"id"`
			Snapshot *decision.UsageSnapshot `json:"snapshot"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dashboard); err != nil {
		t.Fatal(err)
	}
	for _, provider := range dashboard.Providers {
		if provider.ID != "cursor-main" {
			continue
		}
		if provider.Snapshot == nil || provider.Snapshot.Weekly.PeriodSeconds != 2592000 {
			t.Fatalf("snapshot = %#v", provider.Snapshot)
		}
		if _, ok := provider.Snapshot.Allowance("model:other_models:monthly"); !ok {
			t.Fatalf("pools missing: %#v", provider.Snapshot.Allowances)
		}
		return
	}
	t.Fatal("cursor-main missing from the dashboard")
}

// The account window (total usage) is far behind pace, but Other Models is
// ahead of pace. The run would draw on Other Models, so the account window
// admitting must not override the pool's wait.
func TestCursorMonthlyPoolWaitIsNotOverriddenByTheAccountWindow(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(5, 70, 5, 15*24*time.Hour))
	createCursorCandidate(t, db, "cursor-opus", "claude-opus-5-5-high", "opus-task", 100)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask != nil {
		t.Fatalf("selected %#v: a waiting monthly pool was overridden", result.SelectedTask)
	}
	assertCursorRejection(t, result, "opus-task", "model:other_models:monthly is waiting")
}

// A pool on its last scrap keeps a floor, so a run cannot start on it and
// spill into on-demand spend.
func TestCursorMonthlyPoolKeepsAReserve(t *testing.T) {
	server, db := newCursorServer(t, cursorLimitsCard(5, 95, 5, 15*24*time.Hour))
	createCursorCandidate(t, db, "cursor-opus", "claude-opus-5-5-high", "opus-task", 100)

	result := postJSON[cursorEvaluation](t, server.URL+"/v1/scheduler/evaluate", map[string]any{"provider_account_id": "cursor-main"})
	if result.SelectedTask != nil {
		t.Fatalf("selected %#v on a pool with 5%% left", result.SelectedTask)
	}
	assertCursorRejection(t, result, "opus-task", "model:other_models:monthly reserve is protected")
}

func assertCursorRejection(t *testing.T, result cursorEvaluation, taskID, reason string) {
	t.Helper()
	for _, rejection := range result.Result.CandidateRejections {
		if rejection.TaskID == taskID && strings.Contains(rejection.Reason, reason) {
			return
		}
	}
	t.Fatalf("no %q rejection for %s: %#v", reason, taskID, result.Result.CandidateRejections)
}

// The CLI runs jobs as one Cursor account while usage is read from another:
// headroom on one is no reason to bill the other, so nothing is scheduled and
// the dashboard names the cause.
func TestCursorAccountMismatchStopsScheduling(t *testing.T) {
	openUsage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, cursorLimitsCard(5, 5, 5, 15*24*time.Hour))
	}))
	t.Cleanup(openUsage.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig("http://127.0.0.1:1")
	cfg.Providers["cursor-main"] = config.Provider{
		Provider: "cursor", UsageSource: "openusage", OpenUsageURL: openUsage.URL, WindowWeeklyCost: 0.10,
	}
	apiServer := api.NewServer(cfg, db, func() time.Time { return apiNow })
	apiServer.SetCursorAccountCheck(&cursorusage.LocalAccountCheck{
		AppAccount: func(context.Context) (string, error) { return "app@example.com", nil },
		CLIAccount: func(context.Context) (string, error) { return "cli@example.com", nil },
	})
	server := httptest.NewServer(apiServer)
	t.Cleanup(server.Close)
	createCursorCandidate(t, db, "cursor-auto", "auto", "auto-task", 50)

	response, err := http.Post(server.URL+"/v1/scheduler/evaluate", "application/json",
		strings.NewReader(`{"provider_account_id":"cursor-main"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode == http.StatusOK && strings.Contains(string(body), `"selected_task":{`) {
		t.Fatalf("scheduled despite mismatched accounts: %s", body)
	}
	if strings.Contains(string(body), "example.com") {
		t.Fatalf("an account address leaked: %s", body)
	}

	var dashboard struct {
		Providers []struct {
			ID          string `json:"id"`
			UsageSource struct {
				Reason    string `json:"reason"`
				LastError string `json:"last_error"`
			} `json:"usage_source"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard?fields=providers", &dashboard)
	for _, provider := range dashboard.Providers {
		if provider.ID != "cursor-main" {
			continue
		}
		if provider.UsageSource.Reason != cursorusage.ReasonAccountMismatch ||
			!strings.Contains(provider.UsageSource.LastError, "different accounts") {
			t.Fatalf("usage source = %#v", provider.UsageSource)
		}
		return
	}
	t.Fatal("cursor-main missing from the dashboard")
}
