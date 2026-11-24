package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/config"
)

// creditsHome writes a service config and pins the secret store to a
// private directory, so no test ever touches the login Keychain.
func creditsHome(t *testing.T) (configPath, secretsDir string) {
	t.Helper()
	home := t.TempDir()
	configPath = filepath.Join(home, "redline.yaml")
	if err := os.WriteFile(configPath, []byte(profileServiceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsDir = filepath.Join(home, "secrets")
	t.Setenv(apicredits.SecretsDirEnv, secretsDir)
	return configPath, secretsDir
}

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.RunWithInput(args, strings.NewReader(stdin), &stdout, &stderr, func() time.Time { return time.Now() })
	return code, stdout.String(), stderr.String()
}

func TestAddOpenAICreditsAccountWritesNoSecretToTheAccountsFile(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	code, stdout, stderr := runCLI(t, "sess-SYNTHETICSESSIONKEY000000\n", "--config", configPath,
		"account", "add-openai-credits", "api-credits", "--org", "org-synthetic", "--label", "Linked org",
		"--min-balance", "10", "--pacing", "even", "--session-key-stdin")
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "added account api-credits") || !strings.Contains(stdout, "set-api-key api-credits") {
		t.Fatalf("stdout = %s", stdout)
	}
	accounts, err := config.ReadManagedAccounts(filepath.Join(filepath.Dir(configPath), "accounts.yaml"))
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	account := accounts[0]
	if account.Provider != config.ProviderAPICredits || account.APICredits == nil || account.APICredits.OrganizationID != "org-synthetic" ||
		*account.APICredits.MinPromoBalance != 10 || account.APICredits.Pacing != "even" {
		t.Fatalf("account = %+v %+v", account, account.APICredits)
	}
	file, _ := os.ReadFile(filepath.Join(filepath.Dir(configPath), "accounts.yaml"))
	if strings.Contains(string(file), "sess-") {
		t.Fatal("accounts file contains the session key")
	}
	if strings.Contains(stdout+stderr, "SYNTHETICSESSIONKEY") {
		t.Fatal("output echoes the session key")
	}
	stored, err := apicredits.FileSecretStore{Dir: secretsDir}.Load(t.Context(), "api-credits", apicredits.SecretSessionKey)
	if err != nil || stored != "sess-SYNTHETICSESSIONKEY000000" {
		t.Fatalf("stored = %q, %v", stored, err)
	}
}

func TestSetKeysValidatesTheirKind(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-openai-credits", "api-credits"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	store := apicredits.FileSecretStore{Dir: secretsDir}
	// A session key is refused where the API key goes, and the reverse,
	// with nothing saved and nothing echoed.
	if code, _, stderr := runCLI(t, "sess-WRONGKIND", "--config", configPath, "account", "set-api-key", "api-credits"); code == 0 ||
		strings.Contains(stderr, "WRONGKIND") {
		t.Fatalf("set-api-key accepted a session key: %s", stderr)
	}
	if code, _, stderr := runCLI(t, "sk-proj-WRONGKIND", "--config", configPath, "account", "set-session-key", "api-credits"); code == 0 ||
		strings.Contains(stderr, "WRONGKIND") {
		t.Fatalf("set-session-key accepted an API key: %s", stderr)
	}
	if _, err := store.Load(t.Context(), "api-credits", apicredits.SecretAPIKey); err == nil {
		t.Fatal("a wrong key was saved")
	}
	if code, stdout, stderr := runCLI(t, "sk-proj-RIGHTKIND\n", "--config", configPath, "account", "set-api-key", "api-credits"); code != 0 ||
		strings.Contains(stdout, "RIGHTKIND") {
		t.Fatalf("set-api-key: %d %s %s", code, stdout, stderr)
	}
	if got, _ := store.Load(t.Context(), "api-credits", apicredits.SecretAPIKey); got != "sk-proj-RIGHTKIND" {
		t.Fatalf("api key = %q", got)
	}
	if code, _, stderr := runCLI(t, "sess-RIGHTKIND", "--config", configPath, "account", "set-session-key", "api-credits"); code != 0 {
		t.Fatalf("set-session-key: %s", stderr)
	}
	// Keys are only for credits accounts that exist.
	if code, _, _ := runCLI(t, "sess-X", "--config", configPath, "account", "set-session-key", "claude-main"); code == 0 {
		t.Fatal("saved a session key for a claude account")
	}
	if code, _, _ := runCLI(t, "sess-X", "--config", configPath, "account", "set-session-key", "nobody"); code == 0 {
		t.Fatal("saved a session key for an unknown account")
	}
	// Clearing removes the key.
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "clear-keys", "api-credits"); code != 0 {
		t.Fatalf("clear: %s", stderr)
	}
	if _, err := store.Load(t.Context(), "api-credits", apicredits.SecretSessionKey); err == nil {
		t.Fatal("session key survived clear-keys")
	}
}

func TestAddOpenAICreditsRefusesBadSettings(t *testing.T) {
	configPath, _ := creditsHome(t)
	for name, args := range map[string][]string{
		"bad org":      {"--org", "acme"},
		"bad pacing":   {"--pacing", "greedy"},
		"neg balance":  {"--min-balance", "-1"},
		"email label":  {"--label", "me@example.com"},
		"bad id":       {},
		"relative key": {"--session-key-file", "key.txt"},
	} {
		id := "api-credits"
		if name == "bad id" {
			id = "Bad ID!"
		}
		full := append([]string{"--config", configPath, "account", "add-openai-credits", id}, args...)
		if code, _, _ := runCLI(t, "", full...); code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}
