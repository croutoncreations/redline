package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

const claudeConfig = `
database: redline.db
active_policy: standard
providers:
  claude-main:
    provider: claude
    window_weekly_cost: 0.08
policies:
  standard:
    trigger_margin: 0.02
    rolling_reserve: 0.25
`

// A single-account config written before accounts existed must load exactly
// as it did: no accounts file, no new fields, the same provider.
func TestSingleAccountConfigLoadsUnchanged(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, claudeConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers["claude-main"].Provider != "claude" ||
		cfg.Providers["claude-main"].OpenUsageCard != "" || len(cfg.ManagedAccounts) != 0 {
		t.Fatalf("providers = %#v managed = %#v", cfg.Providers, cfg.ManagedAccounts)
	}
	if want := filepath.Join(filepath.Dir(cfg.ConfigPath), "accounts.yaml"); cfg.AccountsFilePath() != want {
		t.Fatalf("accounts file = %q, want %q", cfg.AccountsFilePath(), want)
	}
}

func TestManagedAccountsMergeBesideConfig(t *testing.T) {
	path := writeConfig(t, claudeConfig)
	accounts := []config.ManagedAccount{
		{ID: "claude-client", Provider: "Claude", Label: "Client", OpenUsageCard: "claude@ab12cd34"},
		{ID: "claude-old", Provider: "claude", OpenUsageCard: "claude@00ff00ff", Disabled: true},
	}
	if err := config.WriteManagedAccounts(filepath.Join(filepath.Dir(path), "accounts.yaml"), accounts); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := cfg.Providers["claude-client"]
	if !ok || client.Provider != "claude" || client.Label != "Client" || client.OpenUsageCard != "claude@ab12cd34" ||
		client.WindowWeeklyCost != 0.08 || !cfg.ManagedAccounts["claude-client"] {
		t.Fatalf("managed account = %#v (managed=%v)", client, cfg.ManagedAccounts)
	}
	if _, live := cfg.Providers["claude-old"]; live {
		t.Fatal("a disabled account must not become a live provider")
	}
	if _, kept := cfg.DisabledAccounts["claude-old"]; !kept {
		t.Fatal("a disabled account must stay listed so it can be re-enabled")
	}
	if cfg.ManagedAccounts["claude-main"] {
		t.Fatal("an account from the config file is not editable from Redline")
	}
}

func TestAccountsFileIsOwnerOnlyAndHasNoCredentialFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := config.WriteManagedAccounts(path, []config.ManagedAccount{{ID: "claude-a", Provider: "claude", OpenUsageCard: "claude"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	for _, forbidden := range []string{"token", "secret", "password", "cookie", "email"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("accounts file mentions %q:\n%s", forbidden, data)
		}
	}
	// A credential-shaped key added by hand is refused, not carried along.
	if err := os.WriteFile(path, []byte("version: 1\naccounts:\n  - id: claude-a\n    provider: claude\n    token: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadManagedAccounts(path); err == nil {
		t.Fatal("unknown keys in the accounts file must be refused")
	}
}

func TestAccountsFileFromNewerRedlineIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := os.WriteFile(path, []byte("version: 99\naccounts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ReadManagedAccounts(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityConflictsAreRefused(t *testing.T) {
	cases := map[string]string{
		"two default logins": `
  claude-a:
    provider: claude
    window_weekly_cost: 0.08
  claude-b:
    provider: claude
    window_weekly_cost: 0.08`,
		"same card twice": `
  claude-a:
    provider: claude
    openusage_card: claude@ab12cd34
    window_weekly_cost: 0.08
  claude-b:
    provider: claude
    openusage_card: CLAUDE@AB12CD34
    window_weekly_cost: 0.08`,
		"bare card collides with default login": `
  claude-a:
    provider: claude
    window_weekly_cost: 0.08
  claude-b:
    provider: claude
    openusage_card: claude
    window_weekly_cost: 0.08`,
	}
	for name, providers := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, strings.Replace(claudeConfig, `
  claude-main:
    provider: claude
    window_weekly_cost: 0.08`, providers, 1)))
			if err != nil {
				t.Fatalf("a duplicate written in the config file loads (as before) with a warning, got %v", err)
			}
		})
	}
}

// Redline never creates a duplicate itself: one involving a managed account
// is refused, even when the other side is in the config file.
func TestManagedAccountDuplicatingAConfigLoginIsRefused(t *testing.T) {
	path := writeConfig(t, claudeConfig)
	if err := config.WriteManagedAccounts(filepath.Join(filepath.Dir(path), "accounts.yaml"),
		[]config.ManagedAccount{{ID: "claude-two", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); !errors.Is(err, config.ErrAccountConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigFileDuplicateIsAWarning(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, strings.Replace(claudeConfig, `
  claude-main:
    provider: claude
    window_weekly_cost: 0.08`, `
  claude-main:
    provider: claude
    window_weekly_cost: 0.08
  claude-b:
    provider: claude
    window_weekly_cost: 0.08`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "same numbers") {
		t.Fatalf("warnings = %q", cfg.Warnings)
	}
}

func TestOneServerSpelledTwoWaysIsOneIdentity(t *testing.T) {
	for _, other := range []string{"http://localhost:6736", "HTTP://127.0.0.1:6736/", "http://[::1]:6736", "http://127.0.0.1:6736/v1"} {
		err := config.ValidateAccountIdentities(map[string]config.Provider{
			"a": {Provider: "claude", OpenUsageURL: "http://127.0.0.1:6736", OpenUsageCard: "claude@ab12cd34"},
			"b": {Provider: "claude", OpenUsageURL: other, OpenUsageCard: "claude@ab12cd34"},
		})
		if !errors.Is(err, config.ErrAccountConflict) {
			t.Errorf("%s: err = %v", other, err)
		}
	}
	// Codex accounts are checked too.
	if err := config.ValidateAccountIdentities(map[string]config.Provider{
		"codex-a": {Provider: "codex"}, "codex-b": {Provider: "codex"},
	}); !errors.Is(err, config.ErrAccountConflict) {
		t.Fatalf("codex duplicate: %v", err)
	}
}

func TestLegacyUsageGoesOnlyToASoleDefaultLoginAccount(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{
		"claude-client": {Provider: "claude", OpenUsageCard: "claude@ab12cd34"},
		"codex-main":    {Provider: "codex"},
	}}
	owners := cfg.LegacyUsageOwners()
	if _, ok := owners["claude"]; ok {
		t.Fatal("history from the default login must not go to a card-bound account")
	}
	if len(owners["codex"]) != 1 {
		t.Fatalf("owners = %v", owners)
	}
}

func TestDistinctCardsAndOneDefaultLoginAreAllowed(t *testing.T) {
	_, err := config.Load(writeConfig(t, strings.Replace(claudeConfig, `
  claude-main:
    provider: claude
    window_weekly_cost: 0.08`, `
  claude-main:
    provider: claude
    window_weekly_cost: 0.08
  claude-client:
    provider: claude
    openusage_card: claude@ab12cd34
    label: Client
    window_weekly_cost: 0.08
  claude-side:
    provider: claude
    openusage_card: claude@0123abcd
    window_weekly_cost: 0.08`, 1)))
	if err != nil {
		t.Fatal(err)
	}
}

func TestAccountFieldValidation(t *testing.T) {
	cases := map[string]string{
		"card from another provider": "    openusage_card: codex@ab12cd34\n",
		"card with native source":    "    openusage_card: claude@ab12cd34\n    usage_source: native\n",
		"card with bad suffix":       "    openusage_card: claude@AB 12\n",
		"email as label":             "    label: someone@example.com\n",
		"overlong label":             "    label: " + strings.Repeat("x", 61) + "\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			configured := strings.Replace(claudeConfig, "    window_weekly_cost: 0.08\n", "    window_weekly_cost: 0.08\n"+extra, 1)
			if _, err := config.Load(writeConfig(t, configured)); err == nil {
				t.Fatalf("expected %s to be refused", name)
			}
		})
	}
}

func TestManagedAccountMayNotShadowConfigAccount(t *testing.T) {
	path := writeConfig(t, claudeConfig)
	if err := config.WriteManagedAccounts(filepath.Join(filepath.Dir(path), "accounts.yaml"),
		[]config.ManagedAccount{{ID: "claude-main", Provider: "claude", OpenUsageCard: "claude@ab12cd34"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); !errors.Is(err, config.ErrAccountConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountIDValidation(t *testing.T) {
	for _, id := range []string{"claude-client", "c1", "a.b_c-d"} {
		if err := config.ValidateAccountID(id); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
	}
	for _, id := range []string{"", "Claude", "-x", "has space", "a/b", strings.Repeat("a", 64)} {
		if err := config.ValidateAccountID(id); err == nil {
			t.Fatalf("%q should be refused", id)
		}
	}
}

func TestCommentOnlyAccountsFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := os.WriteFile(path, []byte("# nothing yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if accounts, err := config.ReadManagedAccounts(path); err != nil || len(accounts) != 0 {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
}
