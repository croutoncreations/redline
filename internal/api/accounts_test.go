package api_test

import (
	"bytes"
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
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

type accountsBody struct {
	Accounts []struct {
		ID            string `json:"id"`
		Provider      string `json:"provider"`
		Label         string `json:"label"`
		OpenUsageCard string `json:"openusage_card"`
		Origin        string `json:"origin"`
		Disabled      bool   `json:"disabled"`
		Running       bool   `json:"running"`
		Pending       string `json:"pending"`
		Status        string `json:"status"`
		SignIn        string `json:"sign_in"`
	} `json:"accounts"`
	RestartRequired bool `json:"restart_required"`
}

func accountsServer(t *testing.T, usageURL string) (*httptest.Server, *store.DB, string) {
	t.Helper()
	directory := t.TempDir()
	db, err := store.Open(filepath.Join(directory, "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := testConfig(usageURL)
	cfg.ConfigPath = filepath.Join(directory, "redline.yaml")
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	t.Cleanup(server.Close)
	return server, db, filepath.Join(directory, "accounts.yaml")
}

func accountRequest(t *testing.T, method, url string, body any) (int, accountsBody, string) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, _ := http.NewRequest(method, url, reader)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(response.Body)
	var parsed accountsBody
	_ = json.Unmarshal(raw.Bytes(), &parsed)
	return response.StatusCode, parsed, raw.String()
}

func TestAccountLifecycleWritesOnlyTheAccountsFile(t *testing.T) {
	server, _, accountsPath := accountsServer(t, "http://127.0.0.1:1")
	status, body, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "claude-client", "provider": "claude", "label": "Client", "usage_source": "openusage",
		"openusage_url": "http://127.0.0.1:6736", "openusage_card": "claude@ab12cd34",
	})
	if status != http.StatusCreated || !body.RestartRequired {
		t.Fatalf("create = %d %s", status, raw)
	}
	var added bool
	for _, account := range body.Accounts {
		if account.ID == "claude-client" {
			added = account.Pending == "add" && account.Origin == "managed" && !account.Running &&
				strings.Contains(account.SignIn, "OpenUsage") && strings.Contains(account.SignIn, "never asks for a password")
		}
		if account.ID == "claude-main" && account.Origin != "config" {
			t.Fatalf("config-file account reported as %q", account.Origin)
		}
	}
	if !added {
		t.Fatalf("new account not listed as pending add: %s", raw)
	}
	info, err := os.Stat(accountsPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file = %v, %v", info, err)
	}
	data, _ := os.ReadFile(accountsPath)
	if !strings.Contains(string(data), "claude@ab12cd34") || strings.Contains(string(data), "token") {
		t.Fatalf("accounts file = %s", data)
	}

	// Edit the label, then disable, then remove.
	status, _, raw = accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/claude-client", map[string]any{"label": "Northwind"})
	if status != http.StatusOK || !strings.Contains(raw, `"label":"Northwind"`) {
		t.Fatalf("edit = %d %s", status, raw)
	}
	status, _, raw = accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/claude-client", map[string]any{"disabled": true})
	if status != http.StatusOK || !strings.Contains(raw, `"disabled":true`) {
		t.Fatalf("disable = %d %s", status, raw)
	}
	status, body, raw = accountRequest(t, http.MethodDelete, server.URL+"/v1/accounts/claude-client", nil)
	if status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, raw)
	}
	for _, account := range body.Accounts {
		if account.ID == "claude-client" {
			t.Fatalf("removed account still listed: %s", raw)
		}
	}
}

func TestAccountConflictsAreRefusedAndNothingIsWritten(t *testing.T) {
	server, _, accountsPath := accountsServer(t, "http://127.0.0.1:1")
	for name, request := range map[string]map[string]any{
		// claude-main already reads the default Claude login.
		"second default login": {"id": "claude-two", "provider": "claude"},
		"bare card is the default login": {"id": "claude-two", "provider": "claude", "openusage_card": "claude",
			"usage_source": "openusage", "openusage_url": "http://127.0.0.1:6736"},
		"config account id": {"id": "claude-main", "provider": "claude", "openusage_card": "claude@ab12cd34"},
	} {
		status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", request)
		if status != http.StatusConflict || !strings.Contains(raw, "account_conflict") {
			t.Errorf("%s: status %d %s", name, status, raw)
		}
	}
	for name, request := range map[string]map[string]any{
		"email label":     {"id": "claude-x", "provider": "claude", "label": "me@example.invalid", "openusage_card": "claude@ab12cd34"},
		"bad card":        {"id": "claude-x", "provider": "claude", "openusage_card": "codex@ab12cd34"},
		"bad id":          {"id": "Claude X", "provider": "claude", "openusage_card": "claude@ab12cd34"},
		"card and native": {"id": "claude-x", "provider": "claude", "usage_source": "native", "openusage_card": "claude@ab12cd34"},
		"unknown field":   {"id": "claude-x", "provider": "claude", "api_key": "x"},
	} {
		if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", request); status != http.StatusBadRequest {
			t.Errorf("%s: status %d %s", name, status, raw)
		}
	}
	if _, err := os.Stat(accountsPath); !os.IsNotExist(err) {
		t.Fatalf("a refused change wrote the accounts file: %v", err)
	}
	// Two managed accounts on one card.
	first := map[string]any{"id": "claude-a", "provider": "claude", "openusage_card": "claude@ab12cd34"}
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", first); status != http.StatusCreated {
		t.Fatalf("first = %d %s", status, raw)
	}
	second := map[string]any{"id": "claude-b", "provider": "claude", "openusage_card": "claude@AB12CD34"}
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", second); status != http.StatusConflict {
		t.Fatalf("duplicate card = %d %s", status, raw)
	}
	// Config-file accounts are not editable from Redline.
	if status, _, _ := accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/claude-main", map[string]any{"label": "x"}); status != http.StatusConflict {
		t.Fatalf("editing a config-file account = %d", status)
	}
	if status, _, _ := accountRequest(t, http.MethodDelete, server.URL+"/v1/accounts/nope", nil); status != http.StatusNotFound {
		t.Fatalf("missing account delete = %d", status)
	}
}

func TestRemovingAnAccountUsedByProfilesIsRefused(t *testing.T) {
	server, db, _ := accountsServer(t, "http://127.0.0.1:1")
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "claude-a", "provider": "claude", "openusage_card": "claude@ab12cd34",
	}); status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, raw)
	}
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: "uses-a", ProviderAccountID: "claude-a", HarnessType: "claude-code", Model: "haiku", WorkspaceProvider: "devx",
		Repository: "/tmp/repo", BaseBranch: "main", CleanupPolicy: "retain",
	}, apiNow); err != nil {
		t.Fatal(err)
	}
	status, _, raw := accountRequest(t, http.MethodDelete, server.URL+"/v1/accounts/claude-a", nil)
	if status != http.StatusConflict || !strings.Contains(raw, "uses-a") || !strings.Contains(raw, "disable it instead") {
		t.Fatalf("delete = %d %s", status, raw)
	}
}

// After a restart the saved account is loaded and runs with its own usage.
func TestManagedAccountLoadsOnRestart(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "redline.yaml")
	if err := os.WriteFile(configPath, []byte(`database: `+filepath.Join(directory, "redline.db")+`
active_policy: standard
providers:
  claude-main:
    provider: claude
    window_weekly_cost: 0.08
policies:
  standard:
    trigger_margin: 0.02
    rolling_reserve: 0.25
    pace_thresholds:
      - time_remaining: 72h
        min_weekly_remaining: 0.5
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteManagedAccounts(filepath.Join(directory, "accounts.yaml"), []config.ManagedAccount{
		{ID: "claude-client", Provider: "claude", Label: "Client", OpenUsageCard: "claude@ab12cd34"},
		{ID: "claude-old", Provider: "claude", Label: "Old", OpenUsageCard: "claude@00ff00ff", Disabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["claude-client"]; got.Label != "Client" || got.OpenUsageCard != "claude@ab12cd34" {
		t.Fatalf("loaded = %+v", got)
	}
	if _, running := cfg.Providers["claude-old"]; running {
		t.Fatal("a disabled account must not run")
	}
	db, err := store.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	status, body, raw := accountRequest(t, http.MethodGet, server.URL+"/v1/accounts", nil)
	if status != http.StatusOK || body.RestartRequired {
		t.Fatalf("list = %d %s", status, raw)
	}
	states := map[string]string{}
	for _, account := range body.Accounts {
		states[account.ID] = account.Origin + "/" + map[bool]string{true: "running", false: "stopped"}[account.Running]
		if account.ID == "claude-client" && account.Status != "no_data" {
			t.Fatalf("a never-sampled account must say no_data, got %q", account.Status)
		}
	}
	if states["claude-main"] != "config/running" || states["claude-client"] != "managed/running" || states["claude-old"] != "managed/stopped" {
		t.Fatalf("states = %v", states)
	}
}

func TestDiscoverListsCardsWithoutIdentityAndMarksLinks(t *testing.T) {
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(twoClaudeCards))
	}))
	defer usage.Close()
	server, _, _ := accountsServer(t, usage.URL)
	response, err := http.Get(server.URL + "/v1/accounts/discover/claude?openusage_url=" + usage.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(response.Body)
	text := raw.String()
	if response.StatusCode != http.StatusOK || strings.Contains(text, "example.invalid") || strings.Contains(text, "displayName") {
		t.Fatalf("discover = %d %s", response.StatusCode, text)
	}
	var body struct {
		Cards []struct {
			ID            string `json:"id"`
			Plan          string `json:"plan"`
			LinkedAccount string `json:"linked_account"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(raw.Bytes(), &body); err != nil || len(body.Cards) != 2 {
		t.Fatalf("cards = %s", text)
	}
	// claude-main has no card: it reads the default login, which is the bare card.
	if body.Cards[0].ID != "claude" || body.Cards[0].LinkedAccount != "claude-main" || body.Cards[1].LinkedAccount != "" {
		t.Fatalf("links = %+v", body.Cards)
	}
	if status, _, _ := accountRequest(t, http.MethodGet, server.URL+"/v1/accounts/discover/claude?openusage_url=http://example.com", nil); status != http.StatusBadRequest {
		t.Fatalf("non-loopback discover URL accepted: %d", status)
	}
}

// The phone and the CLI send a card without a URL; that must work.
func TestCardAccountWithoutURLUsesLocalOpenUsage(t *testing.T) {
	server, _, accountsPath := accountsServer(t, "http://127.0.0.1:1")
	status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "claude-client", "provider": "claude", "label": "Client", "openusage_card": "claude@ab12cd34", "usage_source": "openusage",
	})
	if status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, raw)
	}
	accounts, err := config.ReadManagedAccounts(accountsPath)
	if err != nil || len(accounts) != 1 || accounts[0].ProviderConfig().OpenUsageURL != config.DefaultOpenUsageURL {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
}

func TestAccountURLMustBeLoopback(t *testing.T) {
	server, _, _ := accountsServer(t, "http://127.0.0.1:1")
	for _, url := range []string{"http://example.com:6736", "http://10.0.0.5:6736", "https://127.0.0.1:6736", "http://user@127.0.0.1:6736"} {
		status, _, _ := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
			"id": "claude-x", "provider": "claude", "openusage_card": "claude@ab12cd34", "openusage_url": url,
		})
		if status != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", url, status)
		}
	}
}

func TestDisabledAccountCannotDuplicateAnotherCard(t *testing.T) {
	server, _, _ := accountsServer(t, "http://127.0.0.1:1")
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "claude-a", "provider": "claude", "openusage_card": "claude@ab12cd34",
	}); status != http.StatusCreated {
		t.Fatalf("first = %d %s", status, raw)
	}
	status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "claude-b", "provider": "claude", "openusage_card": "claude@ab12cd34", "disabled": true,
	})
	if status != http.StatusConflict {
		t.Fatalf("disabled duplicate = %d %s", status, raw)
	}
	// A second Codex default login is refused as well.
	if status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{
		"id": "codex-two", "provider": "codex",
	}); status != http.StatusConflict {
		t.Fatalf("codex duplicate = %d %s", status, raw)
	}
}

// OpenUsage words card errors with the account email; the dashboard (which
// reaches the phone and the relay) must never carry it.
func TestCardErrorEmailNeverReachesTheDashboard(t *testing.T) {
	body := `{"schema":"openusage.limits.v1","generatedAt":"2026-07-16T18:00:00Z","providers":{},
	  "errors":[{"providerId":"claude@ab12cd34","message":"Token expired for leak-check@example.invalid; sign in again"}]}`
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer usage.Close()
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig(usage.URL)
	cfg.Providers = map[string]config.Provider{
		"claude-client": {Provider: "claude", UsageSource: "openusage", OpenUsageURL: usage.URL, OpenUsageCard: "claude@ab12cd34", WindowWeeklyCost: .08},
	}
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/providers/claude-client/refresh", strings.NewReader("{}"))
	if response, err := http.DefaultClient.Do(request); err == nil {
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.Contains(string(raw), "example.invalid") {
			t.Fatalf("refresh error leaked an email: %s", raw)
		}
	}
	for _, path := range []string{"/v1/dashboard", "/v1/accounts"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.Contains(string(raw), "example.invalid") || strings.Contains(string(raw), "leak-check") {
			t.Fatalf("%s leaked an email: %s", path, raw)
		}
		if path == "/v1/dashboard" && !strings.Contains(string(raw), "[account]") {
			t.Fatalf("expected the redacted error on the dashboard: %s", raw)
		}
	}
}

// A removed or switched-off managed account cannot gain a profile before the
// restart that applies the change.
func TestProfilesCannotTargetALeavingAccount(t *testing.T) {
	directory := t.TempDir()
	if err := config.WriteManagedAccounts(filepath.Join(directory, "accounts.yaml"), []config.ManagedAccount{
		{ID: "claude-a", Provider: "claude", OpenUsageCard: "claude@ab12cd34"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("http://127.0.0.1:1")
	cfg.ConfigPath = filepath.Join(directory, "redline.yaml")
	cfg.Providers["claude-a"] = config.ManagedAccount{ID: "claude-a", Provider: "claude", OpenUsageCard: "claude@ab12cd34"}.ProviderConfig()
	cfg.ManagedAccounts = map[string]bool{"claude-a": true}
	db, err := store.Open(filepath.Join(directory, "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(api.NewServer(cfg, db, func() time.Time { return apiNow }))
	defer server.Close()
	if status, _, raw := accountRequest(t, http.MethodDelete, server.URL+"/v1/accounts/claude-a", nil); status != http.StatusOK {
		t.Fatalf("delete = %d %s", status, raw)
	}
	encoded, _ := json.Marshal(map[string]any{"id": "late", "provider_account_id": "claude-a", "harness_type": "claude-code",
		"model": "haiku", "workspace_provider": "devx", "repository": t.TempDir(), "base_branch": "main", "cleanup_policy": "retain"})
	response, err := http.Post(server.URL+"/v1/profiles", "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "removed") {
		t.Fatalf("profile on removed account = %d %s", response.StatusCode, raw)
	}
}
