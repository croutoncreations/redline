package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

const testSessionKey = "sess-SYNTHETICSESSIONKEYFORTESTS0000000"

type creditsSecrets struct {
	mu    sync.Mutex
	items map[string]string
}

func (m *creditsSecrets) Load(_ context.Context, account, kind string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value, ok := m.items[account+"/"+kind]; ok {
		return value, nil
	}
	return "", apicredits.ErrSecretNotFound
}
func (m *creditsSecrets) Replace(_ context.Context, account, kind, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[account+"/"+kind] = value
	return nil
}
func (m *creditsSecrets) Clear(_ context.Context, account, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, account+"/"+kind)
	return nil
}

// creditGrantsBody is a synthetic credit_grants response at apiNow: one
// promo grant of $100 with `remaining` left, expiring in expiresIn, plus $8
// of purchased credit.
func creditGrantsBody(remaining float64, expiresIn time.Duration) string {
	expiry := apiNow.Add(expiresIn)
	start := expiry.Add(-30 * 24 * time.Hour)
	return fmt.Sprintf(`{"object":"credit_summary","total_available":%v,"total_paid_available":8,"grants":{"object":"list","data":[
{"id":"chatgpt_api_credits_promo_synthetic","grant_amount":100,"used_amount":%v,"effective_at":%d,"expires_at":%d},
{"id":"auto_recharge_grant_synthetic","grant_amount":8,"used_amount":0,"effective_at":%d,"expires_at":%d}]}}`,
		remaining+8, 100-remaining, start.Unix(), expiry.Unix(), start.Unix(), expiry.Add(300*24*time.Hour).Unix())
}

type creditsFixture struct {
	server  *httptest.Server
	db      *store.DB
	secrets *creditsSecrets
	ran     chan domain.ExecutionProfile
	body    func() string
}

type profileRecorder struct{ ran chan domain.ExecutionProfile }

func (p profileRecorder) Execute(_ context.Context, _ domain.Run, _ domain.Task, profile domain.ExecutionProfile) error {
	p.ran <- profile
	return nil
}

func newCreditsServer(t *testing.T, remaining float64, expiresIn time.Duration, settings *config.APICredits) *creditsFixture {
	t.Helper()
	fixture := &creditsFixture{secrets: &creditsSecrets{items: map[string]string{}}, ran: make(chan domain.ExecutionProfile, 4)}
	body := creditGrantsBody(remaining, expiresIn)
	fixture.body = func() string { return body }
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSessionKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, fixture.body())
	}))
	t.Cleanup(platform.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture.db = db
	cfg := testConfig("http://127.0.0.1:1")
	if settings == nil {
		settings = &config.APICredits{OrganizationID: "org-synthetic"}
	}
	cfg.Providers["api-credits"] = config.Provider{Provider: config.ProviderAPICredits, Label: "API credits",
		WindowWeeklyCost: 0.05, APICredits: settings}
	apiServer := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, profileRecorder{fixture.ran})
	apiServer.SetAPICredits(apicredits.Client{BaseURL: platform.URL, Now: func() time.Time { return apiNow }}, fixture.secrets)
	fixture.server = httptest.NewServer(apiServer)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *creditsFixture) saveKeys(t *testing.T) {
	t.Helper()
	f.secrets.items["api-credits/"+apicredits.SecretSessionKey] = testSessionKey
	f.secrets.items["api-credits/"+apicredits.SecretAPIKey] = "sk-proj-SYNTHETIC"
}

func createCreditsCandidate(t *testing.T, db *store.DB, harness string) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "credits-" + harness, ProviderAccountID: "api-credits", HarnessType: harness, HarnessCommand: "true",
		Model: creditsModel(harness), WorkspaceProvider: "devx",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{
		ID: "credits-task-" + harness, Name: "Credits " + harness, Prompt: "p", ExecutionProfileID: "credits-" + harness,
		Priority: 1, Type: domain.OneOff, DispatchTier: domain.DispatchFiresale,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
}

func creditsModel(harness string) string {
	if harness == "pi" {
		return "openai/gpt-5.6-sol"
	}
	return ""
}

func dashboardProviders(t *testing.T, server *httptest.Server) []map[string]any {
	t.Helper()
	var body struct {
		Providers []map[string]any `json:"providers"`
	}
	response := request(t, server, http.MethodGet, "/v1/dashboard", nil)
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Providers
}

// The dashboard shows a credits account's dollar balance and settings, and
// never the key.
func TestCreditsAccountAppearsOnDashboardWithBalance(t *testing.T) {
	fixture := newCreditsServer(t, 80, 10*24*time.Hour, nil)
	fixture.saveKeys(t)
	response := request(t, fixture.server, http.MethodPost, "/v1/scheduler/evaluate", map[string]any{"provider_account_id": "api-credits"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("evaluate status = %d", response.StatusCode)
	}
	raw := readBody(t, response)
	if strings.Contains(raw, "sess-") || strings.Contains(raw, "sk-proj") {
		t.Fatal("evaluate response leaks a key")
	}
	var credits map[string]any
	for _, provider := range dashboardProviders(t, fixture.server) {
		if provider["id"] == "api-credits" {
			snapshot, _ := provider["snapshot"].(map[string]any)
			credits, _ = snapshot["credits"].(map[string]any)
			if provider["account_status"] != "ok" {
				t.Fatalf("status = %v", provider["account_status"])
			}
		}
	}
	if credits == nil || credits["promo_remaining"] != 80.0 || credits["paid_available"] != 8.0 || credits["min_promo_balance"] != 5.0 {
		t.Fatalf("credits = %v", credits)
	}
}

// A missing session key is "signed out" on the dashboard, with the command
// that fixes it.
func TestCreditsAccountWithoutSessionKeyIsSignedOut(t *testing.T) {
	fixture := newCreditsServer(t, 80, 10*24*time.Hour, nil)
	_ = request(t, fixture.server, http.MethodPost, "/v1/scheduler/evaluate", map[string]any{"provider_account_id": "api-credits"})
	for _, provider := range dashboardProviders(t, fixture.server) {
		if provider["id"] != "api-credits" {
			continue
		}
		source, _ := provider["usage_source"].(map[string]any)
		if provider["account_status"] != "signed_out" || !strings.Contains(fmt.Sprint(source["last_error"]), "set-session-key api-credits") {
			t.Fatalf("provider = %v", provider)
		}
		return
	}
	t.Fatal("credits account missing from dashboard")
}

// Near expiry, a pi job on the account runs with exactly the account's key
// and organization.
func TestCreditsFiresaleDispatchesWithTheAccountsKey(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	response := request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"})
	decodeResponse(t, response, &result)
	if result.Run == nil || !result.Result.Firesale {
		t.Fatalf("result = %+v", result.Result)
	}
	select {
	case profile := <-fixture.ran:
		if profile.APICreditsVendor != "openai" || profile.APICreditsKey != "sk-proj-SYNTHETIC" || profile.APICreditsOrganization != "org-synthetic" {
			t.Fatalf("run profile vendor=%q key=%q org=%q", profile.APICreditsVendor, profile.APICreditsKey, profile.APICreditsOrganization)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
}

// The floor is a start gate, so it also counts what runs already in flight
// may still spend: each one reserves run_cost_estimate (default $1). With
// $6 promo left and a $5 floor, one run fits; a second would risk reaching
// paid credit, so it waits until the first finishes.
func TestCreditsFloorReservesRunsAlreadyInFlight(t *testing.T) {
	fixture := newCreditsServer(t, 6, 24*time.Hour, nil)
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	if err := fixture.db.CreateTask(t.Context(), domain.Task{
		ID: "credits-task-pi-2", Name: "Credits pi 2", Prompt: "p", ExecutionProfileID: "credits-pi",
		Priority: 1, Type: domain.OneOff, DispatchTier: domain.DispatchFiresale,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.SetProviderMaxConcurrentRuns(t.Context(), "api-credits", 4); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.db.AdmitTask(t.Context(), "in-flight", "credits-task-pi-2", "api-credits", "", apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.MarkRunRunning(t.Context(), run.ID, domain.Workspace{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || result.Result.Decision != decision.Wait || !strings.Contains(result.Result.Reason, "in flight") {
		t.Fatalf("result = %+v", result.Result)
	}
	// The balance is above the floor, so dashboards must not say "at the floor".
	if strings.Contains(result.Result.Reason, "floor") {
		t.Fatalf("reason %q reads as being at the floor", result.Result.Reason)
	}
	// The candidates view agrees with dispatch.
	var candidates struct {
		DispatchAvailable bool   `json:"dispatch_available"`
		ProviderReason    string `json:"provider_reason"`
	}
	decodeResponse(t, request(t, fixture.server, http.MethodGet, "/v1/providers/api-credits/candidates", nil), &candidates)
	if candidates.DispatchAvailable || !strings.Contains(candidates.ProviderReason, "in flight") {
		t.Fatalf("candidates = %+v", candidates)
	}
}

// The reservation is re-checked when the run is admitted, so two dispatches
// that both decided to run before either was admitted can't both start.
func TestCreditsAdmissionRechecksTheReservation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"a", "b"} {
		createTaskForAdmission(t, db, id)
	}
	limits := store.AdmissionLimits{Provider: 4, MaxActiveForReservation: 1}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-a", "task-a", "api-credits", "", nil, limits, apiNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-b", "task-b", "api-credits", "", nil, limits, apiNow); err == nil {
		t.Fatal("second admission ignored the reservation")
	}
}

func createTaskForAdmission(t *testing.T, db *store.DB, id string) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "p-" + id, ProviderAccountID: "api-credits", HarnessType: "command",
		HarnessCommand: "true", WorkspaceProvider: "devx"}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "task-" + id, Name: id, Prompt: "p", ExecutionProfileID: "p-" + id,
		Priority: 1, Type: domain.OneOff, DispatchTier: domain.DispatchFiresale}, apiNow); err != nil {
		t.Fatal(err)
	}
}

// With nothing in flight the same balance starts one run.
func TestCreditsFloorStartsOneRunWhenNothingIsInFlight(t *testing.T) {
	fixture := newCreditsServer(t, 6, 24*time.Hour, nil)
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run == nil {
		t.Fatalf("result = %+v", result.Result)
	}
}

// Far from expiry, firesale pacing holds the balance.
func TestCreditsHoldUntilNearExpiry(t *testing.T) {
	fixture := newCreditsServer(t, 80, 10*24*time.Hour, nil)
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || result.Result.Decision != decision.Wait {
		t.Fatalf("result = %+v", result.Result)
	}
}

// Without a saved API key there is nothing safe to run with: work holds
// rather than inheriting whatever OPENAI_API_KEY the service has.
func TestCreditsWithoutAPIKeyHoldsWork(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	fixture.secrets.items["api-credits/"+apicredits.SecretSessionKey] = testSessionKey
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "set-api-key") {
		t.Fatalf("result = %+v", result.Result)
	}
}

// A Claude personal key (sk-ant-usr…) saved on an OpenAI account holds work:
// it starts with sk- but is not an OpenAI key.
func TestCreditsWithClaudePersonalKeyHoldsWork(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	fixture.saveKeys(t)
	fixture.secrets.items["api-credits/"+apicredits.SecretAPIKey] = "sk-ant-usr01-SYNTHETICPERSONAL"
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "is a Claude (Anthropic) key") || strings.Contains(result.Result.Reason, "SYNTHETIC") {
		t.Fatalf("result = %+v", result.Result)
	}
}

// Codex and Claude cannot spend API credits; their profiles are refused on a
// credits account, so a job can never be scheduled against a balance it
// does not draw on.
func TestCreditsAccountRefusesHarnessesThatCannotSpendCredits(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	for _, harness := range []string{"codex-cli", "claude-code"} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": "bad-" + harness, "provider_account_id": "api-credits", "harness_type": harness, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "API credits") {
			t.Fatalf("%s: status = %d body = %s", harness, response.StatusCode, body)
		}
	}
	response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
		"id": "ok-pi", "provider_account_id": "api-credits", "harness_type": "pi", "model": "openai/gpt-5.6-sol", "workspace_provider": "devx",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("pi profile status = %d: %s", response.StatusCode, readBody(t, response))
	}
}

// A pi run bills whichever provider its model names; only openai/* models use
// the injected API key. Subscription routes (openai-codex/*, anthropic-cli/*)
// and pi's own default model would be scheduled against credits they never
// spend, so they are refused.
func TestCreditsAccountRefusesPiModelsThatDoNotBillTheAPIKey(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	for _, model := range []string{"", "default", "openai-codex/gpt-5.6-sol", "anthropic-cli/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6", "gpt-5.6-sol"} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": "pi-" + strings.NewReplacer("/", "-", ".", "-").Replace(model) + "x", "provider_account_id": "api-credits",
			"harness_type": "pi", "model": model, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "openai/") {
			t.Fatalf("model %q: status = %d body = %s", model, response.StatusCode, body)
		}
	}
}

func request(t *testing.T, server *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeResponse(t *testing.T, response *http.Response, into any) {
	t.Helper()
	body := readBody(t, response)
	if response.StatusCode >= 300 {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), into); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// Credits accounts and their keys are set up only on the Redline machine;
// the HTTP API (reachable from paired phones) refuses to create one.
func TestCreditsAccountCannotBeCreatedOverHTTP(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	response := request(t, fixture.server, http.MethodPost, "/v1/accounts", map[string]any{"id": "more-credits", "provider": "openai-api-credits"})
	body := readBody(t, response)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "add-openai-credits") {
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}
	for _, field := range []string{"session_key", "api_key", "api_credits"} {
		response = request(t, fixture.server, http.MethodPost, "/v1/accounts", map[string]any{"id": "x", "provider": "codex", field: "sess-x"})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "unknown field") {
			t.Fatalf("%s: status = %d body = %s", field, response.StatusCode, body)
		}
	}
}

// A codex profile left on a credits account (saved before the rule, or from
// the config file) is passed over with its reason; other jobs still run.
func TestCreditsAccountSkipsMismatchedProfileAndRunsTheRest(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	fixture.saveKeys(t)
	if err := fixture.db.CreateProfile(t.Context(), domain.ExecutionProfile{ID: "legacy-codex", ProviderAccountID: "api-credits",
		HarnessType: "codex-cli", WorkspaceProvider: "devx"}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.CreateTask(t.Context(), domain.Task{ID: "legacy", Name: "legacy", Prompt: "p", ExecutionProfileID: "legacy-codex",
		Priority: 99, Type: domain.OneOff, DispatchTier: domain.DispatchFiresale}, apiNow); err != nil {
		t.Fatal(err)
	}
	createCreditsCandidate(t, fixture.db, "pi")
	var result struct {
		schedulerResponseForTest
	}
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run == nil || result.SelectedTask == nil || result.SelectedTask.ID != "credits-task-pi" {
		t.Fatalf("result = %+v", result.Result)
	}
	rejected := false
	for _, rejection := range result.Result.CandidateRejections {
		rejected = rejected || (rejection.TaskID == "legacy" && strings.Contains(rejection.Reason, "pi or command"))
	}
	if !rejected {
		t.Fatalf("rejections = %+v", result.Result.CandidateRejections)
	}
}

// Paid credit alone never dispatches, even near expiry.
func TestCreditsPaidOnlyBalanceNeverDispatches(t *testing.T) {
	fixture := newCreditsServer(t, 0, 24*time.Hour, nil)
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || result.Result.Decision == decision.Admit {
		t.Fatalf("result = %+v", result.Result)
	}
}

// The user's floor holds at the server too.
func TestCreditsFloorHoldsDispatch(t *testing.T) {
	floor := 50.0
	fixture := newCreditsServer(t, 40, 24*time.Hour, &config.APICredits{MinPromoBalance: &floor})
	fixture.saveKeys(t)
	createCreditsCandidate(t, fixture.db, "pi")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "$50.00 floor") {
		t.Fatalf("result = %+v", result.Result)
	}
}

// openai/ alone names no model, and --api-key would put a key in argv.
func TestCreditsAccountRefusesPiModelWithoutAnIDAndAPIKeyArgs(t *testing.T) {
	fixture := newCreditsServer(t, 80, 24*time.Hour, nil)
	for i, tc := range []struct {
		model, want string
		args        []string
	}{
		{"openai/", "openai/", nil},
		{"openai/ ", "openai/", nil},
		{"openai/gpt-5.6-sol", "unsupported", []string{"--api-key=k"}},
	} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": fmt.Sprintf("pi-bad-%d", i), "provider_account_id": "api-credits", "harness_type": "pi", "model": tc.model,
			"harness_args": tc.args, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Fatalf("%q %v: status = %d body = %s", tc.model, tc.args, response.StatusCode, body)
		}
	}
}
