package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/config"
)

const syntheticConsoleOrg = "22222222-2222-4222-8222-222222222222"

func TestAccountHelpListsTheClaudeCreditsCommand(t *testing.T) {
	configPath, _ := creditsHome(t)
	_, stdout, stderr := runCLI(t, "", "--config", configPath, "account")
	help := stdout + stderr
	for _, want := range []string{"add-claude-credits <id>", "sk-ant-sid", "sk-ant-api", "platform.claude.com"} {
		if !strings.Contains(help, want) {
			t.Errorf("account help does not mention %q:\n%s", want, help)
		}
	}
}

func TestAddClaudeCreditsAccountStoresSettingsAndSessionKey(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	code, stdout, stderr := runCLI(t, "sk-ant-sid01-SYNTHETICSESSIONKEY\n", "--config", configPath,
		"account", "add-claude-credits", "claude-credits", "--organization-id", syntheticConsoleOrg, "--label", "Console org",
		"--min-balance", "3", "--pacing", "even", "--firesale-window", "24h", "--session-key-stdin")
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "added account claude-credits reading Claude API credits") ||
		!strings.Contains(stdout, "set-api-key claude-credits") || !strings.Contains(stdout, "sk-ant-api") ||
		!strings.Contains(stdout, "workspace") {
		t.Fatalf("stdout = %s", stdout)
	}
	if strings.Contains(stdout+stderr, "SYNTHETICSESSIONKEY") {
		t.Fatal("output echoes the session key")
	}
	accounts, err := config.ReadManagedAccounts(filepath.Join(filepath.Dir(configPath), "accounts.yaml"))
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	account := accounts[0]
	if account.Provider != config.ProviderAnthropicAPICredits || account.APICredits == nil ||
		account.APICredits.OrganizationID != syntheticConsoleOrg || *account.APICredits.MinPromoBalance != 3 ||
		account.APICredits.Pacing != "even" || account.APICredits.FiresaleWindow != "24h" {
		t.Fatalf("account = %+v %+v", account, account.APICredits)
	}
	file, _ := os.ReadFile(filepath.Join(filepath.Dir(configPath), "accounts.yaml"))
	if strings.Contains(string(file), "sk-ant") {
		t.Fatal("accounts file contains the session key")
	}
	stored, err := apicredits.FileSecretStore{Dir: secretsDir}.Load(t.Context(), "claude-credits", apicredits.SecretSessionKey)
	if err != nil || stored != "sk-ant-sid01-SYNTHETICSESSIONKEY" {
		t.Fatalf("stored = %q, %v", stored, err)
	}
}

func TestAddClaudeCreditsRefusesBadSettings(t *testing.T) {
	configPath, _ := creditsHome(t)
	for name, args := range map[string][]string{
		"openai org":  {"--organization-id", "org-abc"},
		"bad pacing":  {"--pacing", "greedy"},
		"neg balance": {"--min-balance", "-1"},
	} {
		full := append([]string{"--config", configPath, "account", "add-claude-credits", "claude-credits"}, args...)
		if code, _, _ := runCLI(t, "", full...); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if code, _, stderr := runCLI(t, "sess-OPENAI\n", "--config", configPath, "account", "add-claude-credits", "claude-credits",
		"--session-key-stdin"); code == 0 || !strings.Contains(stderr, "OpenAI") || strings.Contains(stderr, "sess-OPENAI") {
		t.Fatalf("accepted an OpenAI session key: %s", stderr)
	}
}

// set-session-key, set-api-key and clear-keys check each key against the
// account's vendor, naming an obvious mix-up.
func TestClaudeCreditsKeysAreValidatedPerVendor(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-claude-credits", "claude-credits"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-openai-credits", "openai-credits"); code != 0 {
		t.Fatalf("add openai: %s", stderr)
	}
	store := apicredits.FileSecretStore{Dir: secretsDir}
	for _, tc := range []struct {
		account, command, key, mention string
	}{
		{"claude-credits", "set-api-key", "sk-proj-OPENAIKEY", "OpenAI"},
		{"claude-credits", "set-api-key", "sk-ant-sid01-WRONGKIND", "session key"},
		{"claude-credits", "set-session-key", "sess-OPENAIKEY", "OpenAI"},
		{"claude-credits", "set-session-key", "sk-ant-api03-WRONGKIND", "API key"},
		{"openai-credits", "set-api-key", "sk-ant-api03-CLAUDEKEY", "Claude"},
		{"openai-credits", "set-session-key", "sk-ant-sid01-CLAUDEKEY", "Claude"},
	} {
		code, _, stderr := runCLI(t, tc.key+"\n", "--config", configPath, "account", tc.command, tc.account)
		if code == 0 || !strings.Contains(stderr, tc.mention) || strings.Contains(stderr, "KEY\n") || strings.Contains(stderr, tc.key) {
			t.Errorf("%s %s %s: code %d stderr %s", tc.account, tc.command, tc.key[:10], code, stderr)
		}
	}
	if _, err := store.Load(t.Context(), "claude-credits", apicredits.SecretAPIKey); err == nil {
		t.Fatal("a wrong key was saved")
	}
	if code, stdout, stderr := runCLI(t, "sk-ant-api03-RIGHTKIND\n", "--config", configPath, "account", "set-api-key", "claude-credits"); code != 0 ||
		strings.Contains(stdout, "RIGHTKIND") || !strings.Contains(stdout, "Claude") {
		t.Fatalf("set-api-key: %d %s %s", code, stdout, stderr)
	}
	if code, _, stderr := runCLI(t, "sk-ant-sid01-RIGHTKIND\n", "--config", configPath, "account", "set-session-key", "claude-credits"); code != 0 {
		t.Fatalf("set-session-key: %s", stderr)
	}
	if got, _ := store.Load(t.Context(), "claude-credits", apicredits.SecretAPIKey); got != "sk-ant-api03-RIGHTKIND" {
		t.Fatalf("api key = %q", got)
	}
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "clear-keys", "claude-credits"); code != 0 {
		t.Fatalf("clear: %s", stderr)
	}
	for _, kind := range []string{apicredits.SecretSessionKey, apicredits.SecretAPIKey} {
		if _, err := store.Load(t.Context(), "claude-credits", kind); err == nil {
			t.Fatalf("%s survived clear-keys", kind)
		}
	}
}

const syntheticLookupSessionKey = "sk-ant-sid01-SYNTHETICLOOKUPKEY"

// fakeConsoleOrganizations serves GET /api/organizations to the CLI's
// organization lookup and counts every request it receives.
func fakeConsoleOrganizations(t *testing.T, status int, body string) *int {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/organizations" || r.Header.Get("Cookie") != "sessionKey="+syntheticLookupSessionKey {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	cli.UseConsoleForTest(t, server.URL, server.Client())
	return &requests
}

const syntheticOrganizations = `[{"uuid":"11111111-1111-4111-8111-111111111111","name":"Synthetic Chat","capabilities":["chat"]},
{"uuid":"` + syntheticConsoleOrg + `","name":"Personal Console","capabilities":["api","api_individual"]}]`

// addExistingClaudeCredits saves a first Claude credits account so a second
// one needs its own organization.
func addExistingClaudeCredits(t *testing.T, configPath, org string) {
	t.Helper()
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-claude-credits", "claude-credits",
		"--organization-id", org); code != 0 {
		t.Fatalf("add first: %s", stderr)
	}
}

func readAccounts(t *testing.T, configPath string) []config.ManagedAccount {
	t.Helper()
	accounts, err := config.ReadManagedAccounts(filepath.Join(filepath.Dir(configPath), "accounts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func assertNoSessionKeySaved(t *testing.T, secretsDir, id string) {
	t.Helper()
	if _, err := (apicredits.FileSecretStore{Dir: secretsDir}).Load(t.Context(), id, apicredits.SecretSessionKey); err == nil {
		t.Fatalf("a session key was saved for %s", id)
	}
}

// With an existing Claude credits account, piping the new login's session
// key finds its Console organization: one command adds the account.
func TestAddClaudeCreditsFindsTheOrganizationFromTheSessionKey(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	addExistingClaudeCredits(t, configPath, "33333333-3333-4333-8333-333333333333")
	requests := fakeConsoleOrganizations(t, http.StatusOK, syntheticOrganizations)
	code, stdout, stderr := runCLI(t, syntheticLookupSessionKey+"\n", "--config", configPath,
		"account", "add-claude-credits", "claude-credits-2", "--session-key-stdin", "--label", "Personal")
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d", *requests)
	}
	if !strings.Contains(stdout, "added account claude-credits-2 reading Claude API credits for "+syntheticConsoleOrg+" (Personal Console)") {
		t.Fatalf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "set-session-key") || !strings.Contains(stdout, "set-api-key claude-credits-2") {
		t.Fatalf("next steps = %s", stdout)
	}
	if strings.Contains(stdout+stderr, "SYNTHETICLOOKUPKEY") {
		t.Fatal("output echoes the session key")
	}
	accounts := readAccounts(t, configPath)
	if len(accounts) != 2 || accounts[1].ID != "claude-credits-2" || accounts[1].Label != "Personal" ||
		accounts[1].APICredits == nil || accounts[1].APICredits.OrganizationID != syntheticConsoleOrg {
		t.Fatalf("accounts = %+v", accounts)
	}
	stored, err := apicredits.FileSecretStore{Dir: secretsDir}.Load(t.Context(), "claude-credits-2", apicredits.SecretSessionKey)
	if err != nil || stored != syntheticLookupSessionKey {
		t.Fatalf("stored = %q, %v", stored, err)
	}
}

// Every failed lookup says what to do, and saves neither the account nor
// the key.
func TestAddClaudeCreditsLookupFailuresSaveNothing(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       []string
	}{
		{"rejected session", `{"error":"` + syntheticLookupSessionKey + `"}`, http.StatusUnauthorized,
			[]string{"rejected the session key", "fresh sessionKey cookie"}},
		{"forbidden session", `{}`, http.StatusForbidden, []string{"rejected the session key"}},
		{"no API organization", `[{"uuid":"11111111-1111-4111-8111-111111111111","name":"Synthetic Chat","capabilities":["chat"]}]`, http.StatusOK,
			[]string{"no organization on that Claude login has API access", "Settings › Billing"}},
		{"several API organizations", `[{"uuid":"11111111-1111-4111-8111-111111111111","name":"Team One","capabilities":["api"]},
			{"uuid":"` + syntheticConsoleOrg + `","name":"Team Two","capabilities":["api"]}]`, http.StatusOK,
			[]string{"11111111-1111-4111-8111-111111111111  Team One", syntheticConsoleOrg + "  Team Two", "--organization-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath, secretsDir := creditsHome(t)
			addExistingClaudeCredits(t, configPath, "33333333-3333-4333-8333-333333333333")
			fakeConsoleOrganizations(t, tc.status, tc.body)
			code, stdout, stderr := runCLI(t, syntheticLookupSessionKey+"\n", "--config", configPath,
				"account", "add-claude-credits", "claude-credits-2", "--session-key-stdin")
			if code == 0 {
				t.Fatalf("accepted: %s", stdout)
			}
			for _, want := range tc.want {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			if strings.Contains(stdout+stderr, "SYNTHETICLOOKUPKEY") {
				t.Fatal("output echoes the session key")
			}
			if accounts := readAccounts(t, configPath); len(accounts) != 1 {
				t.Fatalf("accounts = %+v", accounts)
			}
			assertNoSessionKeySaved(t, secretsDir, "claude-credits-2")
		})
	}
}

func TestAddClaudeCreditsLookupNetworkErrorSavesNothing(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	cli.UseConsoleForTest(t, server.URL, nil)
	code, _, stderr := runCLI(t, syntheticLookupSessionKey+"\n", "--config", configPath,
		"account", "add-claude-credits", "claude-credits", "--session-key-stdin")
	if code == 0 || !strings.Contains(stderr, "could not reach platform.claude.com") || !strings.Contains(stderr, "--organization-id") {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if accounts := readAccounts(t, configPath); len(accounts) != 0 {
		t.Fatalf("accounts = %+v", accounts)
	}
	assertNoSessionKeySaved(t, secretsDir, "claude-credits")
}

// The discovered organization is checked like a typed one: one already
// read by another account is refused, naming that account.
func TestAddClaudeCreditsRefusesADiscoveredOrganizationAlreadyRead(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	addExistingClaudeCredits(t, configPath, syntheticConsoleOrg)
	fakeConsoleOrganizations(t, http.StatusOK, syntheticOrganizations)
	code, _, stderr := runCLI(t, syntheticLookupSessionKey+"\n", "--config", configPath,
		"account", "add-claude-credits", "claude-credits-2", "--session-key-stdin")
	if code == 0 || !strings.Contains(stderr, `"claude-credits"`) || !strings.Contains(stderr, syntheticConsoleOrg) {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if accounts := readAccounts(t, configPath); len(accounts) != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
	assertNoSessionKeySaved(t, secretsDir, "claude-credits-2")
}

// A typed --organization-id is used as given: no lookup, and the piped key
// is still saved.
func TestAddClaudeCreditsWithOrganizationIDSkipsTheLookup(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	addExistingClaudeCredits(t, configPath, "33333333-3333-4333-8333-333333333333")
	requests := fakeConsoleOrganizations(t, http.StatusOK, syntheticOrganizations)
	code, stdout, stderr := runCLI(t, syntheticLookupSessionKey+"\n", "--config", configPath,
		"account", "add-claude-credits", "claude-credits-2", "--organization-id", syntheticConsoleOrg, "--session-key-stdin")
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if *requests != 0 {
		t.Fatalf("requests = %d, want no lookup", *requests)
	}
	if !strings.Contains(stdout, "for "+syntheticConsoleOrg+"\n") {
		t.Fatalf("stdout = %s", stdout)
	}
	stored, err := apicredits.FileSecretStore{Dir: secretsDir}.Load(t.Context(), "claude-credits-2", apicredits.SecretSessionKey)
	if err != nil || stored != syntheticLookupSessionKey {
		t.Fatalf("stored = %q, %v", stored, err)
	}
}

// Without a key or an organization, a second account is refused with the
// one-command form.
func TestAddClaudeCreditsRefusalShowsTheOneCommandForm(t *testing.T) {
	configPath, _ := creditsHome(t)
	addExistingClaudeCredits(t, configPath, syntheticConsoleOrg)
	requests := fakeConsoleOrganizations(t, http.StatusOK, syntheticOrganizations)
	code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-claude-credits", "claude-credits-2", "--label", "Personal")
	want := "pbpaste | redline --config '" + configPath + "' account add-claude-credits claude-credits-2 --session-key-stdin"
	if code == 0 || !strings.Contains(stderr, want) || !strings.Contains(stderr, `"claude-credits"`) ||
		!strings.Contains(stderr, "--organization-id") {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if *requests != 0 {
		t.Fatalf("requests = %d", *requests)
	}
	if accounts := readAccounts(t, configPath); len(accounts) != 1 {
		t.Fatalf("accounts = %+v", accounts)
	}
}
