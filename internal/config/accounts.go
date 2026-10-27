package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// AccountsFileVersion is the schema version written to the managed accounts
// file. A file from a newer Redline with a higher version is refused rather
// than half-read and then rewritten without the fields it did not know.
const AccountsFileVersion = 1

// ManagedAccount is one account configured from Redline itself. It carries
// only what Redline needs to meter and label the account. There is no field
// for a token, password, cookie, email or any other credential, by design:
// authentication stays with the provider's own CLI or app (and with OpenUsage,
// which reads those logins), and Redline only names which account to watch.
type ManagedAccount struct {
	WeeklyPolicy   string   `yaml:"weekly_policy,omitempty" json:"weekly_policy,omitempty"`
	SessionReserve *float64 `yaml:"session_reserve,omitempty" json:"session_reserve,omitempty"`
	ID             string   `yaml:"id" json:"id"`
	Provider       string   `yaml:"provider" json:"provider"`
	Label          string   `yaml:"label,omitempty" json:"label,omitempty"`
	UsageSource    string   `yaml:"usage_source,omitempty" json:"usage_source,omitempty"`
	OpenUsageURL   string   `yaml:"openusage_url,omitempty" json:"openusage_url,omitempty"`
	OpenUsageCard  string   `yaml:"openusage_card,omitempty" json:"openusage_card,omitempty"`
	// ClaudeConfigDir is a Claude Code profile directory (CLAUDE_CONFIG_DIR).
	// Only the directory's path is stored: the login stays in Claude Code's
	// own keychain item for that directory and is read, never copied.
	ClaudeConfigDir  string  `yaml:"claude_config_dir,omitempty" json:"claude_config_dir,omitempty"`
	WindowWeeklyCost float64 `yaml:"window_weekly_cost,omitempty" json:"window_weekly_cost,omitempty"`
	Policy           string  `yaml:"policy,omitempty" json:"policy,omitempty"`
	// Disabled keeps the account on file but out of scheduling, monitoring
	// and the usage screens. It is the safe alternative to removing an
	// account that tasks still reference.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

type accountsFile struct {
	Version  int              `yaml:"version"`
	Accounts []ManagedAccount `yaml:"accounts"`
}

// ErrAccountConflict reports an account that would collide with another one:
// the same ID, or the same metering identity, so two accounts would show (and
// schedule from) the same numbers.
var ErrAccountConflict = errors.New("account conflict")

// DefaultOpenUsageURL is OpenUsage's local API address.
const DefaultOpenUsageURL = "http://127.0.0.1:6736"

var accountIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// openUsageCardSuffix matches OpenUsage's account-card suffix. Upstream mints
// "<family>@<hash8>" from a SHA-256 of the account identity; a salted id on a
// hash-prefix collision stays in the same alphabet.
var openUsageCardSuffix = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// defaultWindowWeeklyCost mirrors config.example.yaml's bootstrap values for
// accounts added without one. Redline learns the observed ratio once enough
// paired snapshots exist, so this only matters for the first days.
var defaultWindowWeeklyCost = map[string]float64{"claude": 0.08, "codex": 0.10}

// ValidateAccountID checks an account ID chosen through the CLI or API. IDs
// appear in URLs, task profiles and the database, so they are kept to a
// conservative lowercase alphabet.
func ValidateAccountID(id string) error {
	if !accountIDPattern.MatchString(id) {
		return fmt.Errorf("account id %q must be 1-63 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit", id)
	}
	return nil
}

func validOpenUsageCard(provider, card string) bool {
	family := strings.ToLower(strings.TrimSpace(provider))
	card = strings.ToLower(strings.TrimSpace(card))
	if card == family {
		return true
	}
	suffix, ok := strings.CutPrefix(card, family+"@")
	return ok && openUsageCardSuffix.MatchString(suffix)
}

// validateLabel keeps labels to short, printable display text. An "@" is
// refused so an email address is not stored as a label by habit; the label is
// shown on a phone that may be relayed through a third party.
func validateLabel(label string) error {
	if label == "" {
		return nil
	}
	if utf8.RuneCountInString(label) > 60 {
		return fmt.Errorf("label must be at most 60 characters")
	}
	if strings.TrimSpace(label) != label {
		return fmt.Errorf("label must not start or end with spaces")
	}
	if strings.Contains(label, "@") {
		return fmt.Errorf("label must not contain '@'; use a nickname rather than an email address")
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("label must contain only printable characters")
		}
	}
	return nil
}

// MeteringIdentity names the data an account's usage comes from. Two enabled
// accounts with the same identity would display, and schedule against, the
// same numbers under two names, which is exactly what multi-account support
// must not do.
func MeteringIdentity(provider Provider) string {
	family := strings.ToLower(strings.TrimSpace(provider.Provider))
	// A Claude Code profile directory is its own login, keyed by Claude Code
	// on the directory's path, so the cleaned absolute path is the identity.
	if dir := NormalizeClaudeConfigDir(provider.ClaudeConfigDir); dir != "" {
		return "claude-profile:" + dir
	}
	// OpenUsage's bare family card ("claude") is the account signed in at
	// the default home, the same login the legacy route and the native
	// collector read, so it is the same identity as no card at all.
	if card := strings.ToLower(strings.TrimSpace(provider.OpenUsageCard)); card != "" && card != family {
		return "openusage:" + normalizeOpenUsageURL(provider.OpenUsageURL) + "#" + card
	}
	// Without a card, OpenUsage's legacy route and the native collector both
	// read the one default login for the provider on this Mac.
	return "default:" + family
}

// ValidateAccountIdentities refuses configurations where two accounts would
// read the same usage. Cursor is skipped: it already has its own one-account
// guard, and this release adds multi-account support for Claude first.
func ValidateAccountIdentities(providers map[string]Provider) error {
	_, err := accountIdentityConflicts(providers, nil)
	return err
}

// accountIdentityConflicts finds accounts that would read the same usage.
// A pair defined entirely in the main config file is returned as a warning,
// since Redline accepted such configs before accounts were isolated and
// refusing to start would be a regression; any pair involving an account
// managed from Redline is an error, so Redline never creates one.
func accountIdentityConflicts(providers map[string]Provider, managed map[string]bool) ([]string, error) {
	var warnings []string
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	owners := make(map[string]string, len(ids))
	for _, id := range ids {
		provider := providers[id]
		// Cursor has its own single-account guard.
		if strings.EqualFold(provider.Provider, "cursor") {
			continue
		}
		identity := MeteringIdentity(provider)
		if other, taken := owners[identity]; taken {
			var err error
			if strings.HasPrefix(identity, "default:") {
				err = fmt.Errorf("%w: accounts %q and %q would both read the default %s login on this Mac; give each extra account its own openusage_card or claude_config_dir",
					ErrAccountConflict, other, id, provider.Provider)
			} else if dir, isProfile := strings.CutPrefix(identity, "claude-profile:"); isProfile {
				err = fmt.Errorf("%w: accounts %q and %q both read Claude Code profile %s", ErrAccountConflict, other, id, DisplayClaudeConfigDir(dir))
			} else {
				err = fmt.Errorf("%w: accounts %q and %q both read OpenUsage card %q", ErrAccountConflict, other, id, provider.OpenUsageCard)
			}
			if managed != nil && !managed[id] && !managed[other] {
				warnings = append(warnings, err.Error()+" (both show the same numbers)")
				continue
			}
			return warnings, err
		}
		owners[identity] = id
	}
	if err := mixedClaudeSources(providers, ids); err != nil {
		return warnings, err
	}
	return warnings, nil
}

// mixedClaudeSources refuses Claude accounts read from both non-default
// OpenUsage cards and Claude Code profile directories. A card ID is an
// opaque hash and a profile is a directory, so nothing safely says whether
// card claude@ab12cd34 and ~/.claude-cc-alpha are the same person: mixing
// the two could show, and schedule against, one account twice. Use one
// source for the extra Claude accounts.
func mixedClaudeSources(providers map[string]Provider, ids []string) error {
	var card, profile string
	for _, id := range ids {
		provider := providers[id]
		if !strings.EqualFold(strings.TrimSpace(provider.Provider), "claude") {
			continue
		}
		switch identity := MeteringIdentity(provider); {
		case strings.HasPrefix(identity, "claude-profile:") && profile == "":
			profile = id
		case strings.HasPrefix(identity, "openusage:") && card == "":
			card = id
		}
	}
	if card != "" && profile != "" {
		return fmt.Errorf("%w: account %q reads an OpenUsage card and account %q reads a Claude Code profile; "+
			"Redline cannot tell whether they are the same Claude account, so extra Claude accounts must all come from one source",
			ErrAccountConflict, card, profile)
	}
	return nil
}

// normalizeOpenUsageURL reduces the ways of writing the local OpenUsage
// address to one, so two spellings of one server cannot pass as two
// identities: scheme and host lowercased, localhost and ::1 read as
// 127.0.0.1, the default port filled in, and any path dropped.
func normalizeOpenUsageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultOpenUsageURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(strings.TrimRight(raw, "/"))
	}
	scheme := strings.ToLower(parsed.Scheme)
	host, port := strings.ToLower(parsed.Hostname()), parsed.Port()
	if host == "localhost" || host == "::1" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = map[string]string{"https": "443"}[scheme]
		if port == "" {
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// LegacyUsageOwners names, per provider, the account that inherits usage
// recorded before accounts were tracked. That history came from the default
// login, so it goes only to a provider's sole account, and only when that
// account still reads the default login.
func (c Config) LegacyUsageOwners() map[string][]string {
	owners := make(map[string][]string)
	for provider, ids := range c.AccountsByProvider() {
		if len(ids) == 1 && strings.HasPrefix(MeteringIdentity(c.Providers[ids[0]]), "default:") {
			owners[provider] = ids
		}
	}
	return owners
}

// AccountsFilePath resolves where managed accounts are stored.
func (c Config) AccountsFilePath() string {
	path := strings.TrimSpace(c.AccountsFile)
	if path == "" {
		if c.ConfigPath == "" {
			return ""
		}
		return filepath.Join(filepath.Dir(c.ConfigPath), "accounts.yaml")
	}
	if !filepath.IsAbs(path) && c.ConfigPath != "" {
		path = filepath.Join(filepath.Dir(c.ConfigPath), path)
	}
	return path
}

// ReadManagedAccounts reads the accounts file. A missing file is an empty list.
func ReadManagedAccounts(path string) ([]ManagedAccount, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read accounts file: %w", err)
	}
	var file accountsFile
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		// Empty or comments only: no accounts, not a broken file.
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("decode accounts file %s: %w", path, err)
	}
	if file.Version > AccountsFileVersion {
		return nil, fmt.Errorf("accounts file %s has version %d, newer than this Redline supports (%d)", path, file.Version, AccountsFileVersion)
	}
	return file.Accounts, nil
}

// WriteManagedAccounts replaces the accounts file atomically with owner-only
// permissions. A crash leaves either the old file or the new one, never half.
func WriteManagedAccounts(path string, accounts []ManagedAccount) error {
	if path == "" {
		return fmt.Errorf("accounts file location is unknown")
	}
	sorted := append([]ManagedAccount(nil), accounts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	data, err := yaml.Marshal(accountsFile{Version: AccountsFileVersion, Accounts: sorted})
	if err != nil {
		return fmt.Errorf("encode accounts file: %w", err)
	}
	header := []byte("# Managed by Redline (`redline account …`). Holds no credentials.\n")
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create accounts directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".accounts-*.yaml")
	if err != nil {
		return fmt.Errorf("create accounts file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect accounts file: %w", err)
	}
	if _, err := temporary.Write(append(header, data...)); err != nil {
		temporary.Close()
		return fmt.Errorf("write accounts file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync accounts file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close accounts file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace accounts file: %w", err)
	}
	return nil
}

// ProviderConfig converts a managed account into the provider configuration
// the scheduler and collectors use.
func (a ManagedAccount) ProviderConfig() Provider {
	cost := a.WindowWeeklyCost
	if cost == 0 {
		cost = defaultWindowWeeklyCost[strings.ToLower(a.Provider)]
	}
	if cost == 0 {
		cost = 0.10
	}
	openUsageURL := a.OpenUsageURL
	if strings.TrimSpace(openUsageURL) == "" && (strings.EqualFold(strings.TrimSpace(a.UsageSource), "openusage") || a.OpenUsageCard != "") {
		// The conventional local OpenUsage address, which is also what an
		// empty URL means everywhere else.
		openUsageURL = DefaultOpenUsageURL
	}
	return Provider{
		Provider: strings.ToLower(strings.TrimSpace(a.Provider)), UsageSource: a.UsageSource,
		OpenUsageURL: openUsageURL, OpenUsageCard: a.OpenUsageCard, ClaudeConfigDir: a.ClaudeConfigDir, Label: a.Label,
		WindowWeeklyCost: cost, Policy: a.Policy,
		WeeklyPolicy: a.WeeklyPolicy, SessionReserve: a.SessionReserve,
	}
}

// NormalizeClaudeConfigDir is the one spelling of a Claude Code profile
// directory: "~/" expanded and the path cleaned, so a trailing slash or a
// tilde cannot pass as a second login. Claude Code names the profile's
// keychain item from this same absolute path. "" stays "".
func NormalizeClaudeConfigDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Clean(dir)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	return filepath.Clean(dir)
}

// DisplayClaudeConfigDir shows a profile directory with the home folder as
// "~", so the user name on the Mac is not repeated on every surface.
func DisplayClaudeConfigDir(dir string) string {
	dir = NormalizeClaudeConfigDir(dir)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(dir, filepath.Clean(home)+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return dir
}

// validateClaudeConfigDir checks a profile directory setting. A profile is a
// separate login, so it is read only by the native collector: OpenUsage and
// the "auto" fallback would read a different account (the default card) and
// show its numbers under this account's name.
func validateClaudeConfigDir(provider Provider) error {
	raw := strings.TrimSpace(provider.ClaudeConfigDir)
	if raw == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(provider.Provider), "claude") {
		return fmt.Errorf("claude_config_dir is only for claude accounts")
	}
	if source := strings.ToLower(strings.TrimSpace(provider.UsageSource)); source != "" && source != "native" {
		return fmt.Errorf("claude_config_dir reads the profile's own login, so usage_source must be native (or empty), not %q", source)
	}
	if strings.TrimSpace(provider.OpenUsageCard) != "" {
		return fmt.Errorf("claude_config_dir and openusage_card name two different sources; use one")
	}
	if strings.ContainsRune(raw, 0) || strings.ContainsAny(raw, "\r\n") {
		return fmt.Errorf("claude_config_dir must be a plain path")
	}
	dir := NormalizeClaudeConfigDir(raw)
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("claude_config_dir %q must be an absolute path or start with ~/", raw)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// The default profile is the default login: no directory needed,
		// and naming it would read it twice under two names.
		if dir == filepath.Join(filepath.Clean(home), ".claude") {
			return fmt.Errorf("claude_config_dir %q is the default Claude Code profile; leave claude_config_dir empty for it", raw)
		}
	}
	return nil
}

// mergeManagedAccounts adds the accounts file to the configured providers.
// Accounts defined in the main config always win and stay read-only from
// Redline's account commands; an accounts-file entry with the same ID is a
// conflict rather than a silent override.
func (c *Config) mergeManagedAccounts() error {
	accounts, err := ReadManagedAccounts(c.AccountsFilePath())
	if err != nil {
		return err
	}
	return c.applyManagedAccounts(accounts)
}

func (c *Config) applyManagedAccounts(accounts []ManagedAccount) error {
	if len(accounts) == 0 {
		return nil
	}
	if c.Providers == nil {
		c.Providers = make(map[string]Provider)
	}
	c.ManagedAccounts = make(map[string]bool)
	c.DisabledAccounts = make(map[string]ManagedAccount)
	seen := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		if err := ValidateAccountID(account.ID); err != nil {
			return fmt.Errorf("accounts file: %w", err)
		}
		if seen[account.ID] {
			return fmt.Errorf("accounts file: %w: account %q is listed twice", ErrAccountConflict, account.ID)
		}
		seen[account.ID] = true
		if _, defined := c.Providers[account.ID]; defined {
			return fmt.Errorf("accounts file: %w: account %q is already defined in the config file", ErrAccountConflict, account.ID)
		}
		if err := account.ProviderConfig().validateWeeklyPolicy(); err != nil {
			return fmt.Errorf("account %q: %w", account.ID, err)
		}
		if account.Disabled {
			c.DisabledAccounts[account.ID] = account
			continue
		}
		c.Providers[account.ID] = account.ProviderConfig()
		c.ManagedAccounts[account.ID] = true
	}
	return nil
}

// AccountsByProvider lists configured account IDs per provider type, sorted.
func (c Config) AccountsByProvider() map[string][]string {
	result := make(map[string][]string)
	for id, provider := range c.Providers {
		key := strings.ToLower(strings.TrimSpace(provider.Provider))
		result[key] = append(result[key], id)
	}
	for key := range result {
		sort.Strings(result[key])
	}
	return result
}

// WithManagedAccounts returns the configuration as it would load with the
// given accounts file contents, fully validated, without writing anything.
// Accounts from the main config file are kept; previously managed accounts
// are replaced by the new list.
func (c Config) WithManagedAccounts(accounts []ManagedAccount) (Config, error) {
	next := c
	next.Providers = make(map[string]Provider, len(c.Providers))
	for id, provider := range c.Providers {
		if !c.ManagedAccounts[id] {
			next.Providers[id] = provider
		}
	}
	next.API.TrustedHosts = append([]string(nil), c.API.TrustedHosts...)
	next.ManagedAccounts, next.DisabledAccounts = nil, nil
	if err := next.applyManagedAccounts(accounts); err != nil {
		return Config{}, err
	}
	if err := next.validate(false); err != nil {
		return Config{}, err
	}
	return next, nil
}

// ConfigFileAccount reports whether id is defined in the main config file,
// which Redline's account commands never rewrite.
func (c Config) ConfigFileAccount(id string) bool {
	_, ok := c.Providers[id]
	return ok && !c.ManagedAccounts[id]
}
