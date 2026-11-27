package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/config"
)

// These commands run on the machine Redline runs on and edit its files and
// secret store directly. A key is read only from standard input, so it
// never appears in shell history or a process listing, and is never
// printed back. Keys never pass through the HTTP API, so a paired phone
// cannot read or set them.

// creditsVendorText is how the CLI words each API credits vendor.
type creditsVendorText struct {
	command, provider, name, platform string
	orgHelp                           string
	sessionHelp, apiKeyHelp           string
	sessionKeyEndpoint                string
}

var creditsVendors = map[string]creditsVendorText{
	config.APICreditsVendorOpenAI: {
		command: "add-openai-credits", provider: config.ProviderAPICredits, name: "OpenAI API credits", platform: "OpenAI platform",
		orgHelp:            "OpenAI API organization ID (org-…); empty reads the session's default organization",
		sessionHelp:        "Authorization value from the platform's credit_grants request",
		apiKeyHelp:         "an sk-… key from that organization, used by pi and command jobs",
		sessionKeyEndpoint: "OpenAI's credit grants endpoint",
	},
	config.APICreditsVendorAnthropic: {
		command: "add-claude-credits", provider: config.ProviderAnthropicAPICredits, name: "Claude API credits", platform: "Claude Console",
		orgHelp:            "Claude Console organization UUID; with --session-key-stdin and none given, the login's one organization with API access is looked up and saved",
		sessionHelp:        "the sessionKey cookie (sk-ant-sid…) from platform.claude.com",
		apiKeyHelp:         "an sk-ant-api… key from the linked Console org (ideally a dedicated redline workspace with a spend limit), used by claude-code, pi and command jobs",
		sessionKeyEndpoint: "the Claude Console credits endpoints on platform.claude.com",
	},
}

// consoleClient looks up a Claude login's Console organization when an
// account is added. Production uses its defaults; tests point it at a fake.
var consoleClient apicredits.ConsoleClient

func runAccountAddOpenAICredits(args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runAccountAddCredits(config.APICreditsVendorOpenAI, args, configPath, stdin, stdout, stderr)
}

func runAccountAddClaudeCredits(args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runAccountAddCredits(config.APICreditsVendorAnthropic, args, configPath, stdin, stdout, stderr)
}

func runAccountAddCredits(vendor string, args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	text := creditsVendors[vendor]
	orgUsage := "--org org-…"
	if vendor == config.APICreditsVendorAnthropic {
		orgUsage = "--organization-id UUID"
	}
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: redline account "+text.command+" <id> ["+orgUsage+"] [--label L] [--min-balance USD] "+
			"[--pacing firesale|even] [--firesale-window 72h] [--session-key-file PATH] [--api-key-file PATH] [--session-key-stdin]")
		return 1
	}
	id := args[0]
	flags := flag.NewFlagSet("account "+text.command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	org := flags.String("org", "", text.orgHelp)
	organizationID := flags.String("organization-id", "", "same as --org")
	label := flags.String("label", "", "display name (no email addresses)")
	minBalance := flags.Float64("min-balance", config.DefaultAPICreditsMinPromoBalance, "promotional balance (USD) a job needs before it may start")
	pacing := flags.String("pacing", "", "firesale (default: run near expiry) or even (spread across the cycle)")
	firesaleWindow := flags.String("firesale-window", "", "how long before expiry firesale pacing runs work (default 72h)")
	sessionKeyFile := flags.String("session-key-file", "", "absolute path to a private file holding the session key, read on every poll")
	apiKeyFile := flags.String("api-key-file", "", "absolute path to a private file holding the API key runs use")
	sessionKeyStdin := flags.Bool("session-key-stdin", false, "read the "+text.platform+" session key from standard input and save it")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if err := config.ValidateAccountID(id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	organization := strings.TrimSpace(*org)
	if other := strings.TrimSpace(*organizationID); other != "" {
		if organization != "" && organization != other {
			fmt.Fprintln(stderr, "--org and --organization-id name different organizations")
			return 1
		}
		organization = other
	}
	cfg, path, err := loadForAccounts(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	saved, err := config.ReadManagedAccounts(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if cfg.ConfigFileAccount(id) {
		fmt.Fprintf(stderr, "account %q is defined in the config file\n", id)
		return 1
	}
	for _, existing := range saved {
		if existing.ID == id {
			fmt.Fprintf(stderr, "account %q already exists\n", id)
			return 1
		}
	}
	redline := "redline"
	if configPath != defaultConfigPath {
		redline = "redline --config " + shellQuote(configPath)
	}
	var sessionKey, organizationName string
	if *sessionKeyStdin {
		if sessionKey, err = readCreditsKey(stdin, vendor, apicredits.SecretSessionKey); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if sessionKey != "" && organization == "" && vendor == config.APICreditsVendorAnthropic {
		found, err := consoleClient.APIOrganization(context.Background(), sessionKey)
		if err != nil {
			fmt.Fprintln(stderr, consoleLookupMessage(err, redline, id))
			return 1
		}
		organization, organizationName = found.UUID, found.DisplayName()
	}
	settings := &config.APICredits{OrganizationID: organization, Pacing: strings.TrimSpace(*pacing),
		FiresaleWindow: strings.TrimSpace(*firesaleWindow), SessionKeyFile: strings.TrimSpace(*sessionKeyFile),
		APIKeyFile: strings.TrimSpace(*apiKeyFile)}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "min-balance" {
			settings.MinPromoBalance = minBalance
		}
	})
	account := config.ManagedAccount{ID: id, Provider: text.provider, Label: strings.TrimSpace(*label), APICredits: settings}
	next := append(append([]config.ManagedAccount(nil), saved...), account)
	if _, err := cfg.WithManagedAccounts(next); err != nil {
		if other := otherCreditsAccount(cfg, saved, vendor, id); organization == "" && other != "" && errors.Is(err, config.ErrAccountConflict) {
			fmt.Fprintln(stderr, needsOrganizationMessage(vendor, other, redline, id, account.Label))
			return 1
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := config.WriteManagedAccounts(path, next); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sessionKey != "" {
		if err := apicredits.DefaultSecretStore(cfg.ConfigPath).Replace(context.Background(), id, apicredits.SecretSessionKey, sessionKey); err != nil {
			fmt.Fprintf(stderr, "account saved, but the session key was not: %v\n", err)
			return 1
		}
	}
	if vendor == config.APICreditsVendorAnthropic {
		fmt.Fprintf(stdout, "added account %s reading Claude API credits", id)
	} else {
		fmt.Fprintf(stdout, "added account %s reading OpenAI API credit grants", id)
	}
	if settings.OrganizationID != "" {
		fmt.Fprintf(stdout, " for %s", settings.OrganizationID)
	}
	if organizationName != "" {
		fmt.Fprintf(stdout, " (%s)", organizationName)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "\nNext:")
	if sessionKey == "" && settings.SessionKeyFile == "" {
		fmt.Fprintf(stdout, "  pbpaste | %s account set-session-key %s   # %s\n", redline, id, text.sessionHelp)
	}
	if settings.APIKeyFile == "" {
		fmt.Fprintf(stdout, "  pbpaste | %s account set-api-key %s       # %s\n", redline, id, text.apiKeyHelp)
	}
	fmt.Fprintln(stdout, "  Restart the Redline service to apply account changes.")
	fmt.Fprintf(stdout, "\nThe session key carries your full %s access. Redline stores it on this machine only\n", text.platform)
	fmt.Fprintf(stdout, "and sends it nowhere but %s.\n", text.sessionKeyEndpoint)
	if vendor == config.APICreditsVendorAnthropic {
		fmt.Fprintln(stdout, "Claude Code itself can spend these credits. Use a key from a dedicated Console workspace with a")
		fmt.Fprintln(stdout, "spend limit: the limit is the hard cap if anything ever spills past the promotional credit.")
	}
	return 0
}

// consoleLookupMessage turns a failed Console organization lookup into what
// to do next. Its errors are fixed text and never carry the key.
func consoleLookupMessage(err error, redline, id string) string {
	retry := fmt.Sprintf("rerun with --organization-id UUID: %s account add-claude-credits %s --organization-id UUID --session-key-stdin", redline, id)
	var several *apicredits.SeveralConsoleOrganizationsError
	switch {
	case errors.Is(err, apicredits.ErrConsoleSessionExpired):
		return "Claude Console rejected the session key; copy a fresh sessionKey cookie from platform.claude.com (developer tools › Application or Storage › Cookies) and try again"
	case errors.Is(err, apicredits.ErrNoConsoleAPIOrganization):
		return "no organization on that Claude login has API access; link the subscription's API credit in claude.ai Settings › Billing, then try again"
	case errors.As(err, &several):
		var b strings.Builder
		b.WriteString("several organizations on that Claude login have API access:\n")
		for _, org := range several.Organizations {
			b.WriteString("  " + org.UUID)
			if name := org.DisplayName(); name != "" {
				b.WriteString("  " + name)
			}
			b.WriteString("\n")
		}
		b.WriteString("Pick the one with the subscription's API credit and " + retry)
		return b.String()
	case errors.Is(err, apicredits.ErrConsoleConnectionFailed):
		return "could not reach platform.claude.com to find the Console organization; check the connection and try again, or " + retry
	}
	return fmt.Sprintf("could not find the Console organization (%v); try again, or %s", err, retry)
}

// otherCreditsAccount names another API credits account of the same vendor,
// enabled or saved, or "" when there is none.
func otherCreditsAccount(cfg config.Config, saved []config.ManagedAccount, vendor, id string) string {
	var ids []string
	for other, provider := range cfg.Providers {
		if other != id && provider.APICreditsVendor() == vendor {
			ids = append(ids, other)
		}
	}
	for _, account := range saved {
		if account.ID != id && account.ProviderConfig().APICreditsVendor() == vendor {
			ids = append(ids, account.ID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	slices.Sort(ids)
	return ids[0]
}

// needsOrganizationMessage refuses a second credits account of a vendor that
// names no organization, with how to give it one.
func needsOrganizationMessage(vendor, other, redline, id, label string) string {
	if vendor == config.APICreditsVendorOpenAI {
		return fmt.Sprintf("account %q already reads OpenAI API credits, so %s needs its own organization: pass --org org-… (Settings › Organization › General on platform.openai.com)", other, id)
	}
	command := fmt.Sprintf("pbpaste | %s account add-claude-credits %s --session-key-stdin", redline, id)
	if label != "" {
		command += " --label " + shellQuote(label)
	}
	return fmt.Sprintf("account %q already reads Claude API credits, so %s needs its own Console organization.\n"+
		"Copy that login's sessionKey cookie from platform.claude.com and Redline finds the organization:\n  %s\n"+
		"or pass --organization-id UUID.", other, id, command)
}

// runAccountSetKey saves one key for an existing API credits account of
// either vendor, after checking it is that vendor's key of that kind.
func runAccountSetKey(kind string, args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	command := "set-session-key"
	if kind == apicredits.SecretAPIKey {
		command = "set-api-key"
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(stderr, "usage: <key on stdin> | redline account %s <id>\n", command)
		return 1
	}
	id := args[0]
	cfg, provider, err := creditsAccount(configPath, id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	vendor := provider.APICreditsVendor()
	key, err := readCreditsKey(stdin, vendor, kind)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := apicredits.DefaultSecretStore(cfg.ConfigPath).Replace(context.Background(), id, kind, key); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	what := keyDescription(vendor, kind)
	settings := provider.EffectiveAPICredits()
	fileSetting, file := "session_key_file", settings.SessionKeyFile
	if kind == apicredits.SecretAPIKey {
		fileSetting, file = "api_key_file", settings.APIKeyFile
	}
	if file != "" {
		// ResolveSecret reads a configured file before the store.
		fmt.Fprintf(stdout, "saved %s for %s\n", strings.TrimPrefix(what, "an "), id)
		fmt.Fprintf(stderr, "warning: %s is configured for %s and takes precedence; Redline keeps using the key in that file until you update it or remove the setting\n", fileSetting, id)
		return 0
	}
	fmt.Fprintf(stdout, "saved %s for %s; Redline uses it from the next poll, no restart needed\n", strings.TrimPrefix(strings.TrimPrefix(what, "an "), "a "), id)
	return 0
}

func runAccountClearKeys(args []string, configPath string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: redline account clear-keys <id>")
		return 1
	}
	cfg, _, err := creditsAccount(configPath, args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store := apicredits.DefaultSecretStore(cfg.ConfigPath)
	for _, kind := range []string{apicredits.SecretSessionKey, apicredits.SecretAPIKey} {
		if err := store.Clear(context.Background(), args[0], kind); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "removed the saved keys for %s\n", args[0])
	return 0
}

func loadForAccounts(configPath string) (config.Config, string, error) {
	resolved := resolveTokenConfigPath(configPath)
	cfg, err := config.Load(resolved)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("load %s: %w", resolved, err)
	}
	path := cfg.AccountsFilePath()
	if path == "" {
		return config.Config{}, "", fmt.Errorf("account management needs a config file")
	}
	return cfg, path, nil
}

// creditsAccount loads the configuration and confirms id is an API credits
// account, enabled or saved but not yet applied, returning its settings.
func creditsAccount(configPath, id string) (config.Config, config.Provider, error) {
	cfg, path, err := loadForAccounts(configPath)
	if err != nil {
		return config.Config{}, config.Provider{}, err
	}
	if provider, ok := cfg.Providers[id]; ok {
		if !provider.IsAPICredits() {
			return config.Config{}, config.Provider{}, fmt.Errorf("account %q is not an API credits account", id)
		}
		return cfg, provider, nil
	}
	saved, err := config.ReadManagedAccounts(path)
	if err != nil {
		return config.Config{}, config.Provider{}, err
	}
	for _, account := range saved {
		if account.ID == id && account.ProviderConfig().IsAPICredits() {
			return cfg, account.ProviderConfig(), nil
		}
	}
	return config.Config{}, config.Provider{}, fmt.Errorf("no API credits account %q; add one with `redline account add-openai-credits %s` or `redline account add-claude-credits %s`", id, id, id)
}

// Key prefixes the CLI recognises, to name an obvious mix-up.
const (
	openAISessionKeyPrefix = "sess-"
	openAIAPIKeyPrefix     = "sk-"
	claudeSessionKeyPrefix = apicredits.ConsoleSessionKeyPrefix
	claudeAPIKeyPrefix     = "sk-ant-api"
	claudeKeyPrefix        = "sk-ant-"
)

// keyDescription names a vendor's key of one kind, with its article.
func keyDescription(vendor, kind string) string {
	switch {
	case vendor == config.APICreditsVendorAnthropic && kind == apicredits.SecretSessionKey:
		return "a Claude Console session key"
	case vendor == config.APICreditsVendorAnthropic:
		return "a Claude API key"
	case kind == apicredits.SecretSessionKey:
		return "an OpenAI platform session key"
	default:
		return "an OpenAI API key"
	}
}

// readCreditsKey reads one key from standard input and checks it is the
// vendor's key of that kind. Its value never appears in an error.
func readCreditsKey(stdin io.Reader, vendor, kind string) (string, error) {
	what := keyDescription(vendor, kind)
	key, err := readKey(stdin, what)
	if err != nil {
		return "", err
	}
	return key, checkCreditsKey(key, vendor, kind)
}

// checkCreditsKey validates a key's shape loosely, by prefix, naming the
// mix-ups people make: the other vendor's key, or the other kind of key.
func checkCreditsKey(key, vendor, kind string) error {
	what := keyDescription(vendor, kind)
	claude := strings.HasPrefix(key, claudeKeyPrefix)
	if vendor == config.APICreditsVendorAnthropic {
		want, other, otherKind := claudeSessionKeyPrefix, claudeAPIKeyPrefix, "a Claude API key; save it with set-api-key"
		if kind == apicredits.SecretAPIKey {
			want, other, otherKind = claudeAPIKeyPrefix, claudeSessionKeyPrefix, "a Claude Console session key; save it with set-session-key"
		}
		switch {
		case strings.HasPrefix(key, want):
			return nil
		case strings.HasPrefix(key, other):
			return fmt.Errorf("that is %s, not %s (expected %s…)", otherKind, what, want)
		case !claude && (strings.HasPrefix(key, openAISessionKeyPrefix) || strings.HasPrefix(key, openAIAPIKeyPrefix)):
			return fmt.Errorf("that looks like an OpenAI key, but this is a Claude API credits account: it needs %s (%s…)", what, want)
		}
		return fmt.Errorf("that is not %s (expected %s…)", what, want)
	}
	want := openAISessionKeyPrefix
	if kind == apicredits.SecretAPIKey {
		want = openAIAPIKeyPrefix
	}
	if claude {
		return fmt.Errorf("that looks like a Claude (Anthropic) key, but this is an OpenAI API credits account: it needs %s (%s…)", what, want)
	}
	if !strings.HasPrefix(key, want) {
		return fmt.Errorf("that is not %s (expected %s…)", what, want)
	}
	return nil
}

// readKey reads one key from standard input. Its value never appears in an
// error.
func readKey(stdin io.Reader, what string) (string, error) {
	if stdin == nil {
		return "", fmt.Errorf("pipe %s on standard input", what)
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 16<<10)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read %s from standard input", what)
	}
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Bearer "))
	key = strings.TrimPrefix(key, "sessionKey=")
	if key == "" {
		return "", fmt.Errorf("no key on standard input; pipe %s, for example `pbpaste | redline account …`", what)
	}
	if strings.ContainsAny(key, " \t;") {
		return "", fmt.Errorf("that is not %s: it contains spaces or separators", what)
	}
	return key, nil
}
