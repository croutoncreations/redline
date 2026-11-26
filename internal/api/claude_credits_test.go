package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

const (
	testConsoleSessionKey = "sk-ant-sid01-SYNTHETICSESSIONKEYFORTESTS"
	testConsoleOrg        = "22222222-2222-4222-8222-222222222222"
	testClaudeAPIKey      = "sk-ant-api03-SYNTHETIC"
)

// consoleCreditsBody is a synthetic Console prepaid/credits response at
// apiNow: a $100 subscription tranche with `remaining` left, expiring in
// expiresIn, plus $8 of purchased credit.
func consoleCreditsBody(remaining float64, expiresIn time.Duration) string {
	expiry := apiNow.Add(expiresIn)
	start := expiry.Add(-30 * 24 * time.Hour)
	cents := int(remaining * 100)
	return fmt.Sprintf(`{"amount":1,"currency":"USD","balance":{"money":null,"credits":{"amount_minor":1,"exponent":2}},
"tranches":[{"currency":"USD","expires_at":null,"granted_at":%q,"program_id":"purchase",
 "remaining":{"money":null,"credits":{"amount_minor":800,"exponent":2}},"granted":{"money":null,"credits":{"amount_minor":800,"exponent":2}}}],
"promo_tranches":[{"currency":"USD","expires_at":%q,"granted_at":%q,"program_id":"subscription_api_credit",
 "remaining":{"money":null,"credits":{"amount_minor":%d,"exponent":2}},"granted":{"money":null,"credits":{"amount_minor":10000,"exponent":2}}}]}`,
		start.Format(time.RFC3339), expiry.Format(time.RFC3339), start.Format(time.RFC3339), cents)
}

func newClaudeCreditsServer(t *testing.T, remaining float64, expiresIn time.Duration, settings *config.APICredits) *creditsFixture {
	t.Helper()
	fixture := &creditsFixture{secrets: &creditsSecrets{items: map[string]string{}}, ran: make(chan domain.ExecutionProfile, 4)}
	body := consoleCreditsBody(remaining, expiresIn)
	fixture.body = func() string { return body }
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "sessionKey="+testConsoleSessionKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/api/organizations/"+testConsoleOrg+"/prepaid/credits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, fixture.body())
	}))
	t.Cleanup(console.Close)
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture.db = db
	cfg := testConfig("http://127.0.0.1:1")
	if settings == nil {
		settings = &config.APICredits{OrganizationID: testConsoleOrg}
	}
	cfg.Providers["api-credits"] = config.Provider{Provider: config.ProviderAnthropicAPICredits, Label: "Claude credits",
		WindowWeeklyCost: 0.05, APICredits: settings}
	apiServer := api.NewServerWithExecutor(cfg, db, func() time.Time { return apiNow }, profileRecorder{fixture.ran})
	apiServer.SetAPICredits(apicredits.Client{}, fixture.secrets)
	apiServer.SetConsoleCredits(apicredits.ConsoleClient{BaseURL: console.URL, Now: func() time.Time { return apiNow }})
	fixture.server = httptest.NewServer(apiServer)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *creditsFixture) saveClaudeKeys(t *testing.T) {
	t.Helper()
	f.secrets.items["api-credits/"+apicredits.SecretSessionKey] = testConsoleSessionKey
	f.secrets.items["api-credits/"+apicredits.SecretAPIKey] = testClaudeAPIKey
}

func TestClaudeCreditsAccountAppearsOnDashboardWithBalance(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 62.4, 10*24*time.Hour, nil)
	fixture.saveClaudeKeys(t)
	response := request(t, fixture.server, http.MethodPost, "/v1/scheduler/evaluate", map[string]any{"provider_account_id": "api-credits"})
	raw := readBody(t, response)
	if response.StatusCode != http.StatusOK || strings.Contains(raw, "sk-ant") {
		t.Fatalf("evaluate status = %d (or leaks a key)", response.StatusCode)
	}
	for _, provider := range dashboardProviders(t, fixture.server) {
		if provider["id"] != "api-credits" {
			continue
		}
		snapshot, _ := provider["snapshot"].(map[string]any)
		credits, _ := snapshot["credits"].(map[string]any)
		if provider["account_status"] != "ok" || provider["provider"] != config.ProviderAnthropicAPICredits ||
			snapshot["provider"] != config.ProviderAnthropicAPICredits {
			t.Fatalf("provider = %v", provider)
		}
		if credits == nil || credits["promo_remaining"] != 62.4 || credits["paid_available"] != 8.0 {
			t.Fatalf("credits = %v", credits)
		}
		return
	}
	t.Fatal("credits account missing from dashboard")
}

func TestClaudeCreditsAccountWithRejectedSessionIsSignedOut(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 10*24*time.Hour, nil)
	_ = request(t, fixture.server, http.MethodPost, "/v1/scheduler/evaluate", map[string]any{"provider_account_id": "api-credits"})
	for _, provider := range dashboardProviders(t, fixture.server) {
		if provider["id"] != "api-credits" {
			continue
		}
		source, _ := provider["usage_source"].(map[string]any)
		if provider["account_status"] != "signed_out" || !strings.Contains(fmt.Sprint(source["last_error"]), "Claude Console") {
			t.Fatalf("provider = %v", provider)
		}
		return
	}
	t.Fatal("credits account missing from dashboard")
}

func TestClaudeCreditsAccountCannotBeCreatedOverHTTP(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	response := request(t, fixture.server, http.MethodPost, "/v1/accounts", map[string]any{"id": "more-credits", "provider": "anthropic-api-credits"})
	body := readBody(t, response)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "add-claude-credits") {
		t.Fatalf("status = %d body = %s", response.StatusCode, body)
	}
}

func createClaudeCreditsCandidate(t *testing.T, db *store.DB, harness, model string) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "credits-" + harness, ProviderAccountID: "api-credits", HarnessType: harness, HarnessCommand: "true",
		Model: model, WorkspaceProvider: "devx",
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

// Near expiry, claude-code, pi and command jobs run with exactly the
// account's Claude key, tagged with its vendor.
func TestClaudeCreditsFiresaleDispatchesWithTheAccountsKey(t *testing.T) {
	for harness, model := range map[string]string{"claude-code": "claude-sonnet-4-6", "pi": "anthropic/claude-sonnet-4-6", "command": ""} {
		fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
		fixture.saveClaudeKeys(t)
		createClaudeCreditsCandidate(t, fixture.db, harness, model)
		var result schedulerResponseForTest
		decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
		if result.Run == nil || !result.Result.Firesale {
			t.Fatalf("%s: result = %+v", harness, result.Result)
		}
		select {
		case profile := <-fixture.ran:
			if profile.APICreditsVendor != "anthropic" || profile.APICreditsKey != testClaudeAPIKey ||
				profile.APICreditsOrganization != testConsoleOrg || profile.ClaudeConfigDir != "" {
				t.Fatalf("%s: run profile vendor=%q org=%q dir=%q", harness, profile.APICreditsVendor, profile.APICreditsOrganization, profile.ClaudeConfigDir)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: run did not start", harness)
		}
	}
}

// An OpenAI key saved on a Claude account (or anything else that is not an
// sk-ant-api… key) holds work rather than being handed to a run.
func TestClaudeCreditsWithWrongAPIKeyHoldsWork(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	fixture.saveClaudeKeys(t)
	fixture.secrets.items["api-credits/"+apicredits.SecretAPIKey] = "sk-proj-OPENAI"
	createClaudeCreditsCandidate(t, fixture.db, "command", "")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "sk-ant-api") || strings.Contains(result.Result.Reason, "sk-proj-OPENAI") {
		t.Fatalf("result = %+v", result.Result)
	}
	delete(fixture.secrets.items, "api-credits/"+apicredits.SecretAPIKey)
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "no Claude API key") || !strings.Contains(result.Result.Reason, "set-api-key api-credits") {
		t.Fatalf("missing key: result = %+v", result.Result)
	}
}

// Codex, Cursor and Hermes cannot spend Claude API credits; claude-code,
// pi and command can.
func TestClaudeCreditsAccountRefusesHarnessesThatCannotSpendCredits(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	for _, harness := range []string{"codex-cli", "cursor-agent", "hermes"} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": "bad-" + harness, "provider_account_id": "api-credits", "harness_type": harness, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Claude API credits") || !strings.Contains(body, "must be") {
			t.Fatalf("%s: status = %d body = %s", harness, response.StatusCode, body)
		}
	}
	for harness, model := range map[string]string{"claude-code": "", "pi": "anthropic/claude-sonnet-4-6", "command": ""} {
		body := map[string]any{"id": "ok-" + harness, "provider_account_id": "api-credits", "harness_type": harness,
			"model": model, "workspace_provider": "devx"}
		if harness == "command" {
			body["harness_command"] = "true"
		}
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", body)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("%s profile status = %d: %s", harness, response.StatusCode, readBody(t, response))
		}
	}
}

// A pi profile on a Claude credits account must name an anthropic/… model:
// the default model and subscription routes would never bill the key.
func TestClaudeCreditsAccountRefusesPiModelsThatDoNotBillTheAPIKey(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	for _, model := range []string{"", "default", "anthropic-cli/claude-sonnet-4-6", "openai/gpt-5.6-sol", "claude-sonnet-4-6"} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": "pi-" + strings.NewReplacer("/", "-", ".", "-").Replace(model) + "x", "provider_account_id": "api-credits",
			"harness_type": "pi", "model": model, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "anthropic/") {
			t.Fatalf("model %q: status = %d body = %s", model, response.StatusCode, body)
		}
	}
}

// The floor and the in-flight reservation hold a Claude credits account
// exactly as they hold an OpenAI one: $6 promo, $5 floor, $1 per run in
// flight leaves no room for a second run.
func TestClaudeCreditsFloorReservesRunsAlreadyInFlight(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 6, 24*time.Hour, nil)
	fixture.saveClaudeKeys(t)
	createClaudeCreditsCandidate(t, fixture.db, "command", "")
	if err := fixture.db.CreateTask(t.Context(), domain.Task{
		ID: "credits-task-2", Name: "Credits 2", Prompt: "p", ExecutionProfileID: "credits-command",
		Priority: 1, Type: domain.OneOff, DispatchTier: domain.DispatchFiresale,
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.SetProviderMaxConcurrentRuns(t.Context(), "api-credits", 4); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.db.AdmitTask(t.Context(), "in-flight", "credits-task-2", "api-credits", "", apiNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.MarkRunRunning(t.Context(), run.ID, domain.Workspace{Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "in flight") {
		t.Fatalf("result = %+v", result.Result)
	}
}

func TestClaudeCreditsFloorHoldsDispatch(t *testing.T) {
	floor := 50.0
	fixture := newClaudeCreditsServer(t, 40, 24*time.Hour, &config.APICredits{OrganizationID: testConsoleOrg, MinPromoBalance: &floor})
	fixture.saveClaudeKeys(t)
	createClaudeCreditsCandidate(t, fixture.db, "command", "")
	var result schedulerResponseForTest
	decodeResponse(t, request(t, fixture.server, http.MethodPost, "/v1/scheduler/execute", map[string]any{"provider_account_id": "api-credits"}), &result)
	if result.Run != nil || !strings.Contains(result.Result.Reason, "$50.00 floor") {
		t.Fatalf("result = %+v", result.Result)
	}
}

// harness_args that would put the key in argv, choose another provider, or
// load other extensions or settings are refused with a 400.
func TestClaudeCreditsAccountRefusesReroutingHarnessArgs(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	for i, tc := range []struct {
		harness, model string
		args           []string
	}{
		{"pi", "anthropic/claude-sonnet-4-6", []string{"--api-key", "k"}},
		{"pi", "anthropic/claude-sonnet-4-6", []string{"--provider", "anthropic"}},
		{"pi", "anthropic/claude-sonnet-4-6", []string{"-e", "./mine.ts"}},
		{"claude-code", "", []string{"--settings", "{}"}},
		{"claude-code", "", []string{"--setting-sources=user"}},
	} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": fmt.Sprintf("args-%d", i), "provider_account_id": "api-credits", "harness_type": tc.harness,
			"model": tc.model, "harness_args": tc.args, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "unsupported") {
			t.Fatalf("%s %v: status = %d body = %s", tc.harness, tc.args, response.StatusCode, body)
		}
	}
}

// anthropic/ alone names no model.
func TestClaudeCreditsAccountRefusesPiModelWithoutAnID(t *testing.T) {
	fixture := newClaudeCreditsServer(t, 80, 24*time.Hour, nil)
	for i, model := range []string{"anthropic/", "anthropic/  "} {
		response := request(t, fixture.server, http.MethodPost, "/v1/profiles", map[string]any{
			"id": fmt.Sprintf("pi-empty-%d", i), "provider_account_id": "api-credits", "harness_type": "pi", "model": model, "workspace_provider": "devx",
		})
		body := readBody(t, response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "anthropic/") {
			t.Fatalf("model %q: status = %d body = %s", model, response.StatusCode, body)
		}
	}
}
