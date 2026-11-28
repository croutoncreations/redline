package config_test

import (
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

const (
	syntheticConsoleOrg      = "22222222-2222-4222-8222-222222222222"
	otherSyntheticConsoleOrg = "44444444-4444-4444-8444-444444444444"
)

func anthropicCreditsAccount(id, extra string) string {
	return "  " + id + ":\n    provider: anthropic-api-credits\n    window_weekly_cost: 0.05\n" + extra
}

func withAccounts(accounts ...string) string {
	return strings.Replace(validConfig, "policies:\n", strings.Join(accounts, "")+"policies:\n", 1)
}

func TestAPICreditsVendorNamesBothFamilies(t *testing.T) {
	for provider, vendor := range map[string]string{
		"openai-api-credits": config.APICreditsVendorOpenAI, "Anthropic-API-Credits": config.APICreditsVendorAnthropic,
		"claude": "", "codex": "", "": "",
	} {
		if got := config.APICreditsVendor(provider); got != vendor {
			t.Errorf("%q: vendor = %q, want %q", provider, got, vendor)
		}
		if got := (config.Provider{Provider: provider}).IsAPICredits(); got != (vendor != "") {
			t.Errorf("%q: IsAPICredits = %v", provider, got)
		}
	}
	if config.ProviderAPICredits != "openai-api-credits" || config.ProviderAnthropicAPICredits != "anthropic-api-credits" {
		t.Fatal("provider ids changed")
	}
}

func TestAnthropicAPICreditsAccountLoadsWithTheSameSettings(t *testing.T) {
	text := withAccounts(anthropicCreditsAccount("claude-credits",
		"    api_credits:\n      organization_id: "+syntheticConsoleOrg+"\n      min_promo_balance: 7\n      run_cost_estimate: 2\n      pacing: even\n      firesale_window: 24h\n      api_key_file: /keys/api\n      session_key_file: /keys/session\n"))
	cfg, err := config.Load(writeConfig(t, text))
	if err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["claude-credits"]
	if !provider.IsAPICredits() || provider.APICreditsVendor() != config.APICreditsVendorAnthropic {
		t.Fatalf("vendor = %q", provider.APICreditsVendor())
	}
	settings := provider.EffectiveAPICredits()
	if settings.OrganizationID != syntheticConsoleOrg || settings.MinPromoBalance != 7 || settings.RunCostEstimate != 2 ||
		settings.Pacing != "even" || settings.APIKeyFile != "/keys/api" || settings.SessionKeyFile != "/keys/session" {
		t.Fatalf("settings = %+v", settings)
	}
	if provider.EffectiveUsageSource() != "native" {
		t.Fatalf("usage source = %q", provider.EffectiveUsageSource())
	}
	if got := config.MeteringIdentity(provider); got != "anthropic-org:"+syntheticConsoleOrg {
		t.Fatalf("identity = %q", got)
	}
}

// A Console organization is a UUID; an OpenAI org-… id on a Claude account
// (or the reverse) is a mix-up.
func TestAPICreditsOrganizationShapeIsPerVendor(t *testing.T) {
	for name, tc := range map[string]struct {
		text    string
		invalid bool
	}{
		"claude uuid":       {withAccounts(anthropicCreditsAccount("c", "    api_credits:\n      organization_id: "+syntheticConsoleOrg+"\n")), false},
		"claude openai org": {withAccounts(anthropicCreditsAccount("c", "    api_credits:\n      organization_id: org-abc\n")), true},
		"claude junk":       {withAccounts(anthropicCreditsAccount("c", "    api_credits:\n      organization_id: ../x\n")), true},
		"openai uuid":       {withAPICredits("    api_credits:\n      organization_id: " + syntheticConsoleOrg + "\n"), true},
	} {
		_, err := config.Load(writeConfig(t, tc.text))
		if (err != nil) != tc.invalid {
			t.Errorf("%s: err = %v, invalid = %v", name, err, tc.invalid)
		}
	}
}

func TestAnthropicAPICreditsRefusesProfilesAndKeysInConfig(t *testing.T) {
	for name, extra := range map[string]string{
		"claude profile": "    claude_config_dir: /tmp/claude-profile\n",
		"session key":    "    api_credits:\n      session_key: sk-ant-sid01-x\n",
		"api key":        "    api_credits:\n      api_key: sk-ant-api03-x\n",
		"openusage":      "    usage_source: openusage\n    openusage_url: http://127.0.0.1:6736\n",
	} {
		if _, err := config.Load(writeConfig(t, withAccounts(anthropicCreditsAccount("c", extra)))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Duplicate organizations and the unnamed-organization rule apply within
// a vendor: an OpenAI organization and a Console organization can never be
// the same balance.
func TestAPICreditsOrganizationConflictsArePerVendor(t *testing.T) {
	named := func(id, org string) string {
		return anthropicCreditsAccount(id, "    api_credits:\n      organization_id: "+org+"\n")
	}
	if _, err := config.Load(writeConfig(t, withAccounts(named("a", syntheticConsoleOrg), named("b", syntheticConsoleOrg)))); err == nil ||
		!strings.Contains(err.Error(), "anthropic-org:") && !strings.Contains(err.Error(), syntheticConsoleOrg) {
		t.Fatalf("duplicate Console organization: err = %v", err)
	}
	if _, err := config.Load(writeConfig(t, withAccounts(named("a", syntheticConsoleOrg), named("b", otherSyntheticConsoleOrg)))); err != nil {
		t.Fatalf("distinct Console organizations refused: %v", err)
	}
	if _, err := config.Load(writeConfig(t, withAccounts(anthropicCreditsAccount("a", ""), named("b", otherSyntheticConsoleOrg)))); err == nil ||
		!strings.Contains(err.Error(), "organization_id") {
		t.Fatalf("unnamed Console organization beside another: err = %v", err)
	}
	openAIUnnamed := "  openai-credits:\n    provider: openai-api-credits\n    window_weekly_cost: 0.05\n"
	if _, err := config.Load(writeConfig(t, withAccounts(openAIUnnamed, anthropicCreditsAccount("a", "")))); err != nil {
		t.Fatalf("one unnamed account per vendor refused: %v", err)
	}
}

// An Anthropic credits account runs Claude Code, pi and command (each given
// ANTHROPIC_API_KEY); the OpenAI rule is unchanged.
func TestAPICreditsHarnessesArePerVendor(t *testing.T) {
	anthropic := config.Provider{Provider: config.ProviderAnthropicAPICredits}
	for harness, ok := range map[string]bool{"claude-code": true, "pi": true, "command": true, "codex-cli": false, "cursor-agent": false, "hermes": false} {
		if got := anthropic.HarnessSpendsAPICredits(harness); got != ok {
			t.Errorf("anthropic %s: %v, want %v", harness, got, ok)
		}
	}
	if (config.Provider{Provider: config.ProviderAPICredits}).HarnessSpendsAPICredits("claude-code") {
		t.Error("openai credits now allows claude-code")
	}
}
