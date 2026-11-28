package cli_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

func TestAccountHelpListsTheOpenAICreditsCommands(t *testing.T) {
	configPath, _ := creditsHome(t)
	_, stdout, stderr := runCLI(t, "", "--config", configPath, "account")
	help := stdout + stderr
	for _, command := range []string{"add-openai-credits <id>", "set-session-key <id>", "set-api-key <id>", "clear-keys <id>"} {
		if !strings.Contains(help, command) {
			t.Errorf("account help does not mention %q:\n%s", command, help)
		}
	}
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

// The next-step commands name the configuration the account went into, so
// they find the account when it is not the default one.
func TestAddOpenAICreditsNextStepsNameTheSelectedConfig(t *testing.T) {
	configPath, _ := creditsHome(t)
	code, stdout, stderr := runCLI(t, "", "--config", configPath, "account", "add-openai-credits", "api-credits")
	if code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	for _, command := range []string{"set-session-key api-credits", "set-api-key api-credits"} {
		want := "redline --config '" + configPath + "' account " + command
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// A configured key file wins over the store, so saving a key for a kind
// that has one warns that the file stays in use.
func TestSetKeyWarnsWhenAConfiguredFileTakesPrecedence(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if code, _, stderr := runCLI(t, "", "--config", configPath, "account", "add-openai-credits", "api-credits",
		"--api-key-file", keyFile); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	code, stdout, stderr := runCLI(t, "sk-proj-SAVED\n", "--config", configPath, "account", "set-api-key", "api-credits")
	if code != 0 {
		t.Fatalf("set-api-key: %d %s", code, stderr)
	}
	if !strings.Contains(stderr, "api_key_file") || !strings.Contains(stderr, "takes precedence") {
		t.Fatalf("no precedence warning: stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.Contains(stdout, "uses it from the next poll") {
		t.Fatalf("claims the saved key is active: %s", stdout)
	}
	if got, _ := (apicredits.FileSecretStore{Dir: secretsDir}).Load(t.Context(), "api-credits", apicredits.SecretAPIKey); got != "sk-proj-SAVED" {
		t.Fatalf("stored = %q", got)
	}
	// The session key has no file configured: no warning.
	_, _, stderr = runCLI(t, "sess-SAVED\n", "--config", configPath, "account", "set-session-key", "api-credits")
	if strings.Contains(stderr, "takes precedence") {
		t.Fatalf("warned without a session key file: %s", stderr)
	}
}

// OpenAI has no organization lookup: a piped session key with no --org is
// saved as before, and a second account without --org is refused with
// where to find the org ID.
func TestAddOpenAICreditsNeverLooksUpAnOrganization(t *testing.T) {
	configPath, secretsDir := creditsHome(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	cli.UseConsoleForTest(t, server.URL, server.Client())
	if code, _, stderr := runCLI(t, "sess-SYNTHETIC\n", "--config", configPath, "account", "add-openai-credits", "api-credits",
		"--session-key-stdin"); code != 0 {
		t.Fatalf("add: %s", stderr)
	}
	if got, _ := (apicredits.FileSecretStore{Dir: secretsDir}).Load(t.Context(), "api-credits", apicredits.SecretSessionKey); got != "sess-SYNTHETIC" {
		t.Fatalf("stored = %q", got)
	}
	code, _, stderr := runCLI(t, "sess-SECOND\n", "--config", configPath, "account", "add-openai-credits", "api-credits-2", "--session-key-stdin")
	if code == 0 || !strings.Contains(stderr, "pass --org org-… (Settings › Organization › General on platform.openai.com)") ||
		!strings.Contains(stderr, `"api-credits"`) {
		t.Fatalf("code = %d stderr = %s", code, stderr)
	}
	if requests != 0 {
		t.Fatalf("requests = %d", requests)
	}
	if _, err := (apicredits.FileSecretStore{Dir: secretsDir}).Load(t.Context(), "api-credits-2", apicredits.SecretSessionKey); err == nil {
		t.Fatal("saved a key for a refused account")
	}
}
