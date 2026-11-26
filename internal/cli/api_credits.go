package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/croutoncreations/redline/internal/apicredits"
	"github.com/croutoncreations/redline/internal/config"
)

// These commands run on the machine Redline runs on and edit its files and
// secret store directly. A key is read only from standard input, so it
// never appears in shell history or a process listing, and is never
// printed back. Keys never pass through the HTTP API, so a paired phone
// cannot read or set them.

func runAccountAddOpenAICredits(args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: redline account add-openai-credits <id> [--org org-…] [--label L] [--min-balance USD] "+
			"[--pacing firesale|even] [--firesale-window 72h] [--session-key-file PATH] [--api-key-file PATH] [--session-key-stdin]")
		return 1
	}
	id := args[0]
	flags := flag.NewFlagSet("account add-openai-credits", flag.ContinueOnError)
	flags.SetOutput(stderr)
	org := flags.String("org", "", "OpenAI API organization ID (org-…); empty reads the session's default organization")
	label := flags.String("label", "", "display name (no email addresses)")
	minBalance := flags.Float64("min-balance", config.DefaultAPICreditsMinPromoBalance, "promotional balance (USD) a job needs before it may start")
	pacing := flags.String("pacing", "", "firesale (default: run near expiry) or even (spread across the cycle)")
	firesaleWindow := flags.String("firesale-window", "", "how long before expiry firesale pacing runs work (default 72h)")
	sessionKeyFile := flags.String("session-key-file", "", "absolute path to a private file holding the session key, read on every poll")
	apiKeyFile := flags.String("api-key-file", "", "absolute path to a private file holding the API key runs use")
	sessionKeyStdin := flags.Bool("session-key-stdin", false, "read the platform session key (sess-…) from standard input and save it")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if err := config.ValidateAccountID(id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, path, err := loadForAccounts(configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	settings := &config.APICredits{OrganizationID: strings.TrimSpace(*org), Pacing: strings.TrimSpace(*pacing),
		FiresaleWindow: strings.TrimSpace(*firesaleWindow), SessionKeyFile: strings.TrimSpace(*sessionKeyFile),
		APIKeyFile: strings.TrimSpace(*apiKeyFile)}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "min-balance" {
			settings.MinPromoBalance = minBalance
		}
	})
	account := config.ManagedAccount{ID: id, Provider: config.ProviderAPICredits, Label: strings.TrimSpace(*label), APICredits: settings}
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
	next := append(append([]config.ManagedAccount(nil), saved...), account)
	if _, err := cfg.WithManagedAccounts(next); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var sessionKey string
	if *sessionKeyStdin {
		if sessionKey, err = readKey(stdin, "sess-", "an OpenAI platform session key"); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
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
	fmt.Fprintf(stdout, "added account %s reading OpenAI API credit grants", id)
	if settings.OrganizationID != "" {
		fmt.Fprintf(stdout, " for %s", settings.OrganizationID)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "\nNext:")
	redline := "redline"
	if configPath != defaultConfigPath {
		redline = "redline --config " + shellQuote(configPath)
	}
	if sessionKey == "" && settings.SessionKeyFile == "" {
		fmt.Fprintf(stdout, "  pbpaste | %s account set-session-key %s   # Authorization value from the platform's credit_grants request\n", redline, id)
	}
	if settings.APIKeyFile == "" {
		fmt.Fprintf(stdout, "  pbpaste | %s account set-api-key %s       # an sk-… key from that organization, used by pi and command jobs\n", redline, id)
	}
	fmt.Fprintln(stdout, "  Restart the Redline service to apply account changes.")
	fmt.Fprintln(stdout, "\nThe session key carries your full platform dashboard access. Redline stores it on this machine only")
	fmt.Fprintln(stdout, "and sends it nowhere but OpenAI's credit grants endpoint.")
	return 0
}

// runAccountSetKey saves one key for an existing API credits account.
func runAccountSetKey(kind string, args []string, configPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	command, prefix, what := "set-session-key", "sess-", "an OpenAI platform session key"
	if kind == apicredits.SecretAPIKey {
		command, prefix, what = "set-api-key", "sk-", "an OpenAI API key"
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
	key, err := readKey(stdin, prefix, what)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := apicredits.DefaultSecretStore(cfg.ConfigPath).Replace(context.Background(), id, kind, key); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
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
	fmt.Fprintf(stdout, "saved %s for %s; Redline uses it from the next poll, no restart needed\n", strings.TrimPrefix(what, "an "), id)
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
			return config.Config{}, config.Provider{}, fmt.Errorf("account %q is not an OpenAI API credits account", id)
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
	return config.Config{}, config.Provider{}, fmt.Errorf("no OpenAI API credits account %q; add one with `redline account add-openai-credits %s`", id, id)
}

// readKey reads one key from standard input. Its value never appears in an
// error.
func readKey(stdin io.Reader, prefix, what string) (string, error) {
	if stdin == nil {
		return "", fmt.Errorf("pipe %s on standard input", what)
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 16<<10)).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read %s from standard input", what)
	}
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Bearer "))
	if key == "" {
		return "", fmt.Errorf("no key on standard input; pipe %s, for example `pbpaste | redline account …`", what)
	}
	if !strings.HasPrefix(key, prefix) || strings.ContainsAny(key, " \t") {
		return "", fmt.Errorf("that is not %s (expected %s…)", what, prefix)
	}
	return key, nil
}
