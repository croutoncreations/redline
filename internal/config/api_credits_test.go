package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

var apiCreditsConfig = strings.Replace(validConfig, "policies:\n", `  openai-credits:
    provider: openai-api-credits
    label: API credits
    window_weekly_cost: 0.05
policies:
`, 1)

func withAPICredits(extra string) string {
	return strings.Replace(apiCreditsConfig, "    window_weekly_cost: 0.05\n", "    window_weekly_cost: 0.05\n"+extra, 1)
}

func TestAPICreditsAccountLoadsWithDefaults(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, apiCreditsConfig))
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["openai-credits"]
	if !provider.IsAPICredits() {
		t.Fatal("not recognised as an API credits account")
	}
	credits := provider.EffectiveAPICredits()
	if credits.MinPromoBalance != 5 || credits.Pacing != config.APICreditsPacingFiresale {
		t.Fatalf("defaults = %+v, want $5 floor and firesale pacing", credits)
	}
	if credits.FiresaleWindow != 72*time.Hour {
		t.Fatalf("firesale window = %v", credits.FiresaleWindow)
	}
	// No usage source applies: credits are read from the platform only.
	if provider.EffectiveUsageSource() != "native" {
		t.Fatalf("usage source = %q", provider.EffectiveUsageSource())
	}
}

func TestAPICreditsSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		invalid     bool
	}{
		{"explicit settings", "    api_credits:\n      organization_id: org-test123\n      min_promo_balance: 12.5\n      pacing: even\n", false},
		{"zero floor", "    api_credits:\n      min_promo_balance: 0\n", false},
		{"firesale window", "    api_credits:\n      firesale_window: 48h\n", false},
		{"bad firesale window", "    api_credits:\n      firesale_window: soon\n", true},
		{"api key in config", "    api_credits:\n      api_key: sk-proj-abc\n", true},
		{"api key file", "    api_credits:\n      api_key_file: /tmp/api-key\n", false},
		{"relative api key file", "    api_credits:\n      api_key_file: api-key\n", true},
		{"negative floor", "    api_credits:\n      min_promo_balance: -1\n", true},
		{"unknown pacing", "    api_credits:\n      pacing: greedy\n", true},
		{"bad org", "    api_credits:\n      organization_id: acme\n", true},
		{"session key in config", "    api_credits:\n      session_key: sess-abc\n", true},
		{"session key file", "    api_credits:\n      session_key_file: /tmp/key\n", false},
		{"relative key file", "    api_credits:\n      session_key_file: key.txt\n", true},
		{"openusage source", "    usage_source: openusage\n    openusage_url: http://127.0.0.1:6736\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(writeConfig(t, withAPICredits(tc.extra)))
			if (err != nil) != tc.invalid {
				t.Fatalf("err = %v, invalid = %v", err, tc.invalid)
			}
			if err == nil && tc.name == "explicit settings" {
				credits := cfg.Providers["openai-credits"].EffectiveAPICredits()
				if credits.OrganizationID != "org-test123" || credits.MinPromoBalance != 12.5 || credits.Pacing != config.APICreditsPacingEven {
					t.Fatalf("settings = %+v", credits)
				}
			}
			if err == nil && tc.name == "zero floor" && cfg.Providers["openai-credits"].EffectiveAPICredits().MinPromoBalance != 0 {
				t.Fatal("explicit zero floor was defaulted")
			}
		})
	}
}

// api_credits settings belong to API credits accounts only.
func TestAPICreditsSettingsRefusedOnOtherProviders(t *testing.T) {
	text := strings.Replace(validConfig, "    window_weekly_cost: 0.10\n", "    window_weekly_cost: 0.10\n    api_credits:\n      pacing: even\n", 1)
	if _, err := config.Load(writeConfig(t, text)); err == nil {
		t.Fatal("api_credits accepted on a codex account")
	}
}

// Two accounts for one organization would show the same balance twice and
// schedule against it twice.
func TestAPICreditsAccountsForOneOrganizationConflict(t *testing.T) {
	text := strings.Replace(withAPICredits("    api_credits:\n      organization_id: org-same\n"), "policies:\n", `  openai-credits-2:
    provider: openai-api-credits
    window_weekly_cost: 0.05
    api_credits:
      organization_id: org-same
policies:
`, 1)
	if _, err := config.Load(writeConfig(t, text)); err == nil || !strings.Contains(err.Error(), "org-same") {
		t.Fatalf("err = %v", err)
	}
	distinct := strings.Replace(text, "organization_id: org-same\n", "organization_id: org-other\n", 1)
	if _, err := config.Load(writeConfig(t, distinct)); err != nil {
		t.Fatalf("distinct organizations refused: %v", err)
	}
}

// A managed account round-trips its settings; it still never carries the
// session key itself.
func TestManagedAPICreditsRoundTrip(t *testing.T) {
	path := writeConfig(t, validConfig)
	floor := 0.0
	accounts := []config.ManagedAccount{{ID: "api-credits", Provider: config.ProviderAPICredits, Label: "Linked org",
		APICredits: &config.APICredits{OrganizationID: "org-abc", MinPromoBalance: &floor, Pacing: "even", APIKeyFile: "/keys/api"}}}
	if err := config.WriteManagedAccounts(filepath.Join(filepath.Dir(path), "accounts.yaml"), accounts); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	credits := cfg.Providers["api-credits"].EffectiveAPICredits()
	if credits.OrganizationID != "org-abc" || credits.MinPromoBalance != 0 || credits.Pacing != "even" || credits.APIKeyFile != "/keys/api" {
		t.Fatalf("round trip = %+v", credits)
	}
}

// A credits account runs only harnesses that call the OpenAI API with a key
// Redline injects; Codex is excluded by OpenAI's terms for the benefit.
func TestAPICreditsHarnessesExcludeCodexAndClaude(t *testing.T) {
	provider := config.Provider{Provider: config.ProviderAPICredits}
	for harness, ok := range map[string]bool{"pi": true, "command": true, "codex-cli": false, "claude-code": false, "cursor-agent": false, "hermes": false} {
		if got := provider.HarnessSpendsAPICredits(harness); got != ok {
			t.Errorf("%s: %v, want %v", harness, got, ok)
		}
	}
}

// An account without organization_id reads the session's default
// organization, which could be the one another account names, so it is
// only accepted as the sole credits account.
func TestAPICreditsAccountWithoutOrganizationMustBeTheOnlyOne(t *testing.T) {
	second := `  openai-credits-2:
    provider: openai-api-credits
    window_weekly_cost: 0.05
    api_credits:
      organization_id: org-named
policies:
`
	text := strings.Replace(apiCreditsConfig, "policies:\n", second, 1)
	_, err := config.Load(writeConfig(t, text))
	if err == nil || !strings.Contains(err.Error(), "openai-credits") || !strings.Contains(err.Error(), "organization_id") {
		t.Fatalf("err = %v", err)
	}
	if _, err := config.Load(writeConfig(t, apiCreditsConfig)); err != nil {
		t.Fatalf("sole credits account without organization refused: %v", err)
	}
	named := strings.Replace(withAPICredits("    api_credits:\n      organization_id: org-first\n"), "policies:\n", second, 1)
	if _, err := config.Load(writeConfig(t, named)); err != nil {
		t.Fatalf("two named organizations refused: %v", err)
	}
}
