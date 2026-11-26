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
