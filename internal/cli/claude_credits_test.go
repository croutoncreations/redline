package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/apicredits"
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
