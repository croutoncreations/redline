package config

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ProviderAPICredits is an OpenAI API organization's credit grants: the
// monthly API credits an eligible ChatGPT subscription grants to a linked
// API organization. Unlike every other provider it is a dollar balance, not
// a subscription window, and it is spent only by jobs that call the OpenAI
// API with that organization's key.
const ProviderAPICredits = "openai-api-credits"

// ProviderAnthropicAPICredits is a Claude Console organization's prepaid
// credit: the monthly API credit a Claude subscription grants to a linked
// Console organization. It is the second vendor of the API credits family
// and shares its settings, scheduling and display; runs are given the
// organization's ANTHROPIC_API_KEY.
const ProviderAnthropicAPICredits = "anthropic-api-credits"

// API credits vendors, as APICreditsVendor names them.
const (
	APICreditsVendorOpenAI    = "openai"
	APICreditsVendorAnthropic = "anthropic"
)

// APICreditsVendor names the vendor of an API credits provider, or "" for
// any other provider.
func APICreditsVendor(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderAPICredits:
		return APICreditsVendorOpenAI
	case ProviderAnthropicAPICredits:
		return APICreditsVendorAnthropic
	default:
		return ""
	}
}

// API credits pacing modes.
const (
	// APICreditsPacingFiresale holds work until the grant nears expiry with
	// money left, then runs it: credits are used before they lapse.
	APICreditsPacingFiresale = "firesale"
	// APICreditsPacingEven spreads spending across the grant's cycle.
	APICreditsPacingEven = "even"
)

// DefaultAPICreditsMinPromoBalance is the promotional balance, in US
// dollars, below which no new job starts unless configured otherwise.
const DefaultAPICreditsMinPromoBalance = 5.0

// DefaultAPICreditsRunCostEstimate is the promotional credit, in US dollars,
// each run already in flight is assumed to still spend when deciding whether
// another may start above the floor.
const DefaultAPICreditsRunCostEstimate = 1.0

// DefaultAPICreditsFiresaleWindow is how long before expiry firesale pacing
// starts spending a grant.
const DefaultAPICreditsFiresaleWindow = 72 * time.Hour

var (
	organizationIDPattern        = regexp.MustCompile(`^org-[A-Za-z0-9_-]{1,64}$`)
	consoleOrganizationIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// ValidConsoleOrganizationID reports whether id is a Claude Console
// organization UUID.
func ValidConsoleOrganizationID(id string) bool {
	return consoleOrganizationIDPattern.MatchString(id)
}

// APICredits configures an API credits account (either vendor). It never holds the
// platform session key: that lives in the platform secret store, or in the
// file SessionKeyFile names (for example one written by a secret manager).
type APICredits struct {
	// OrganizationID selects the organization: an OpenAI org-… ID (the
	// OpenAI-Organization header; empty reads the session's default
	// organization), or a Claude Console organization UUID (empty reads the
	// one organization with API access).
	OrganizationID string `yaml:"organization_id,omitempty" json:"organization_id,omitempty"`
	// MinPromoBalance is the promotional balance (USD) a job needs before it
	// may start. Nil is DefaultAPICreditsMinPromoBalance.
	MinPromoBalance *float64 `yaml:"min_promo_balance,omitempty" json:"min_promo_balance,omitempty"`
	// RunCostEstimate is the promotional credit (USD) each run already in
	// flight is assumed to still spend: a new run starts only while the
	// balance minus that reservation stays above the floor, so concurrent
	// runs can't all start at the floor and spill into paid credit.
	// Nil is DefaultAPICreditsRunCostEstimate; 0 turns the reservation off.
	RunCostEstimate *float64 `yaml:"run_cost_estimate,omitempty" json:"run_cost_estimate,omitempty"`
	// Pacing is firesale (default: run near expiry) or even (spread
	// spending across the cycle).
	Pacing string `yaml:"pacing,omitempty" json:"pacing,omitempty"`
	// FiresaleWindow is how long before a grant expires firesale pacing
	// starts running work. Default DefaultAPICreditsFiresaleWindow.
	FiresaleWindow string `yaml:"firesale_window,omitempty" json:"firesale_window,omitempty"`
	// APIKeyFile is an absolute path to a private file holding the API key
	// (sk-…) runs use. Without it, the key saved with
	// `redline account set-api-key` is used; with neither, work holds so a
	// run never bills an organization Redline does not see.
	APIKeyFile string `yaml:"api_key_file,omitempty" json:"api_key_file,omitempty"`
	// SessionKeyFile is an absolute path the session key is read from on
	// every poll instead of Redline's own secret store.
	SessionKeyFile string `yaml:"session_key_file,omitempty" json:"session_key_file,omitempty"`
	// SessionKey and APIKey exist only so a key pasted into YAML is refused
	// with a clear message rather than silently ignored. Never stored.
	SessionKey string `yaml:"session_key,omitempty" json:"-"`
	APIKey     string `yaml:"api_key,omitempty" json:"-"`
}

// EffectiveAPICredits is an account's credits settings with defaults.
type EffectiveAPICredits struct {
	OrganizationID  string
	MinPromoBalance float64
	RunCostEstimate float64
	Pacing          string
	FiresaleWindow  time.Duration
	SessionKeyFile  string
	APIKeyFile      string
}

// IsAPICredits reports whether the account is an API credits account of
// either vendor.
func (p Provider) IsAPICredits() bool {
	return APICreditsVendor(p.Provider) != ""
}

// APICreditsVendor is the account's API credits vendor, or "".
func (p Provider) APICreditsVendor() string {
	return APICreditsVendor(p.Provider)
}

// EffectiveAPICredits returns the account's credits settings with defaults.
func (p Provider) EffectiveAPICredits() EffectiveAPICredits {
	result := EffectiveAPICredits{MinPromoBalance: DefaultAPICreditsMinPromoBalance, Pacing: APICreditsPacingFiresale,
		FiresaleWindow: DefaultAPICreditsFiresaleWindow, RunCostEstimate: DefaultAPICreditsRunCostEstimate}
	if p.APICredits == nil {
		return result
	}
	result.OrganizationID = strings.TrimSpace(p.APICredits.OrganizationID)
	if p.APICredits.MinPromoBalance != nil {
		result.MinPromoBalance = *p.APICredits.MinPromoBalance
	}
	if p.APICredits.RunCostEstimate != nil {
		result.RunCostEstimate = *p.APICredits.RunCostEstimate
	}
	if pacing := strings.ToLower(strings.TrimSpace(p.APICredits.Pacing)); pacing != "" {
		result.Pacing = pacing
	}
	if window, err := time.ParseDuration(strings.TrimSpace(p.APICredits.FiresaleWindow)); err == nil && window > 0 {
		result.FiresaleWindow = window
	}
	result.SessionKeyFile = strings.TrimSpace(p.APICredits.SessionKeyFile)
	result.APIKeyFile = strings.TrimSpace(p.APICredits.APIKeyFile)
	return result
}

// HarnessSpendsAPICredits reports whether a harness can draw on API credits.
// Only harnesses that call the vendor's API with the injected organization
// key qualify. On OpenAI, Codex is excluded by the benefit's terms ("Codex
// usage through API keys is not supported"); Claude Code, Cursor and Hermes
// bill elsewhere. On Anthropic, Claude Code runs as `claude -p` with the
// key; Codex, Cursor and Hermes bill elsewhere.
func (p Provider) HarnessSpendsAPICredits(harness string) bool {
	switch harness {
	case "pi", "command":
		return p.IsAPICredits()
	case "claude-code":
		return p.APICreditsVendor() == APICreditsVendorAnthropic
	default:
		return false
	}
}

func validateAPICredits(provider Provider) error {
	if !provider.IsAPICredits() {
		if provider.APICredits != nil {
			return fmt.Errorf("api_credits settings apply only to providers %s and %s", ProviderAPICredits, ProviderAnthropicAPICredits)
		}
		return nil
	}
	id := strings.ToLower(strings.TrimSpace(provider.Provider))
	anthropic := provider.APICreditsVendor() == APICreditsVendorAnthropic
	platform := "the OpenAI platform"
	if anthropic {
		platform = "the Claude Console"
	}
	if source := strings.ToLower(strings.TrimSpace(provider.UsageSource)); source != "" && source != "native" {
		return fmt.Errorf("an %s account reads %s directly; remove usage_source %q", id, platform, provider.UsageSource)
	}
	if strings.TrimSpace(provider.OpenUsageCard) != "" || strings.TrimSpace(provider.ClaudeConfigDir) != "" {
		return fmt.Errorf("an %s account cannot have an OpenUsage card or Claude Code profile", id)
	}
	settings := provider.APICredits
	if settings == nil {
		return nil
	}
	if settings.SessionKey != "" {
		return fmt.Errorf("api_credits session_key is never read from configuration; store it with `redline account set-session-key` or point session_key_file at a private file")
	}
	if settings.APIKey != "" {
		return fmt.Errorf("api_credits api_key is never read from configuration; store it with `redline account set-api-key` or point api_key_file at a private file")
	}
	if raw := strings.TrimSpace(settings.FiresaleWindow); raw != "" {
		if _, err := positiveDuration("api_credits firesale_window", raw); err != nil {
			return err
		}
	}
	if org := strings.TrimSpace(settings.OrganizationID); org != "" {
		if anthropic && !ValidConsoleOrganizationID(org) {
			return fmt.Errorf("api_credits organization_id must be a Claude Console organization UUID")
		}
		if !anthropic && !organizationIDPattern.MatchString(org) {
			return fmt.Errorf("api_credits organization_id must look like org-…")
		}
	}
	if floor := settings.MinPromoBalance; floor != nil && (math.IsNaN(*floor) || math.IsInf(*floor, 0) || *floor < 0) {
		return fmt.Errorf("api_credits min_promo_balance must be zero or more US dollars")
	}
	if estimate := settings.RunCostEstimate; estimate != nil && (math.IsNaN(*estimate) || math.IsInf(*estimate, 0) || *estimate < 0) {
		return fmt.Errorf("api_credits run_cost_estimate must be zero or more US dollars")
	}
	switch strings.ToLower(strings.TrimSpace(settings.Pacing)) {
	case "", APICreditsPacingFiresale, APICreditsPacingEven:
	default:
		return fmt.Errorf("api_credits pacing must be firesale or even")
	}
	for name, file := range map[string]string{"session_key_file": settings.SessionKeyFile, "api_key_file": settings.APIKeyFile} {
		if file = strings.TrimSpace(file); file != "" && !filepath.IsAbs(file) {
			return fmt.Errorf("api_credits %s must be an absolute path", name)
		}
	}
	return nil
}

// apiCreditsIdentity is the metering identity of a credits account: its
// vendor and organization, or the session's default organization when none
// is set. The vendor keeps an OpenAI and a Console organization apart.
func apiCreditsIdentity(provider Provider) string {
	org := strings.ToLower(strings.TrimSpace(provider.EffectiveAPICredits().OrganizationID))
	if org == "" {
		org = "default"
	}
	if provider.APICreditsVendor() == APICreditsVendorAnthropic {
		return "anthropic-org:" + org
	}
	return "openai-org:" + org
}
