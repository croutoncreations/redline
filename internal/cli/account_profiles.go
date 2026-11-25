package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/nativeusage"
)

// Claude Code profiles: separate logins kept in their own CLAUDE_CONFIG_DIR
// directories (for example ~/.claude-cc-work). Both commands run on the Mac
// itself and never go through the API: binding a profile decides which local
// login the service reads, so a paired phone must not be able to do it.
//
// Neither command reads a credential. `profiles` reads each directory's
// non-secret metadata and asks the keychain only whether a login item
// exists; `add-profile` records the directory's path in the accounts file.

const defaultProfilePrefix = ".claude-cc-"

type profileRow struct {
	nativeusage.ClaudeProfile
	Display       string `json:"display"`
	Suggested     string `json:"suggested_id"`
	SuggestLabel  string `json:"suggested_label"`
	LinkedAccount string `json:"linked_account,omitempty"`
	// SameAs names another directory or "default login" signed in to the
	// same Claude account, so it would only show the same numbers twice.
	SameAs string `json:"same_as,omitempty"`
}

func runAccountProfiles(args []string, configPath string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("account profiles", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prefix := flags.String("prefix", defaultProfilePrefix, "directory name prefix in your home folder")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rows, err := claudeProfileRows(context.Background(), home, *prefix, configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		writeJSON(stdout, rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "No Claude Code profiles named ~/%s* found.\n", *prefix)
		return 0
	}
	table := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(table, "PROFILE\tPLAN\tLOGIN\tACCOUNT\tLINKED TO\tSUGGESTED")
	for _, row := range rows {
		login := "stored"
		if !row.LoginStored {
			login = "none"
		}
		account := dash(row.Fingerprint)
		if row.SameAs != "" {
			account += " (same as " + row.SameAs + ")"
		}
		suggested := "-"
		if row.LinkedAccount == "" && row.SameAs == "" {
			suggested = fmt.Sprintf("%s %q", row.Suggested, row.SuggestLabel)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Display, dash(row.Plan), login, account, dash(row.LinkedAccount), suggested)
	}
	_ = table.Flush()
	fmt.Fprintln(stdout, "\nACCOUNT is a fingerprint of the Claude account, not the account itself.")
	fmt.Fprintln(stdout, "Add one with: redline account add-profile <id> --dir <PROFILE> --label <name>")
	return 0
}

func claudeProfileRows(ctx context.Context, home, prefix, configPath string) ([]profileRow, error) {
	profiles, err := nativeusage.FindClaudeProfiles(ctx, home, prefix)
	if err != nil {
		return nil, err
	}
	linked := map[string]string{}
	if cfg, loadErr := config.Load(resolveTokenConfigPath(configPath)); loadErr == nil {
		for id, provider := range cfg.Providers {
			if dir := config.NormalizeClaudeConfigDir(provider.ClaudeConfigDir); dir != "" {
				linked[dir] = id
			}
		}
		if saved, readErr := config.ReadManagedAccounts(cfg.AccountsFilePath()); readErr == nil {
			for _, account := range saved {
				if dir := config.NormalizeClaudeConfigDir(account.ClaudeConfigDir); dir != "" {
					if _, taken := linked[dir]; !taken {
						linked[dir] = account.ID
					}
				}
			}
		}
	}
	defaultFingerprint := nativeusage.DefaultClaudeFingerprint(home)
	firstDir := map[string]string{}
	rows := make([]profileRow, 0, len(profiles))
	for _, profile := range profiles {
		name := strings.TrimPrefix(filepath.Base(profile.Dir), prefix)
		row := profileRow{ClaudeProfile: profile, Display: config.DisplayClaudeConfigDir(profile.Dir),
			Suggested: "claude-" + sanitizeID(name), SuggestLabel: friendlyLabel(name),
			LinkedAccount: linked[profile.Dir]}
		if profile.Fingerprint != "" {
			switch {
			case profile.Fingerprint == defaultFingerprint:
				row.SameAs = "default login"
			case firstDir[profile.Fingerprint] != "":
				row.SameAs = firstDir[profile.Fingerprint]
			default:
				firstDir[profile.Fingerprint] = row.Display
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func runAccountAddProfile(args []string, configPath string, stdout, stderr io.Writer) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: redline account add-profile <id> --dir ~/.claude-cc-NAME [--label L] [--dry-run]")
		return 1
	}
	id := args[0]
	flags := flag.NewFlagSet("account add-profile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "Claude Code profile directory (its CLAUDE_CONFIG_DIR)")
	label := flags.String("label", "", "display name, e.g. Alpha (no email addresses)")
	weeklyPolicy := flags.String("weekly-policy", "", "weekly policy: session_only for seats without a weekly limit; empty holds")
	sessionReserve := flags.Float64("session-reserve", 0.50, "fraction of each 5-hour window protected by session_only (0 <= reserve < 1)")
	dryRun := flags.Bool("dry-run", false, "check everything and show the change without saving it")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if strings.TrimSpace(*dir) == "" {
		fmt.Fprintln(stderr, "--dir is required")
		return 1
	}
	if err := config.ValidateAccountID(id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resolved := resolveTokenConfigPath(configPath)
	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "load %s: %v\n", resolved, err)
		return 1
	}
	account := config.ManagedAccount{ID: id, Provider: "claude", Label: strings.TrimSpace(*label),
		UsageSource: "native", ClaudeConfigDir: config.NormalizeClaudeConfigDir(*dir), WeeklyPolicy: *weeklyPolicy}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "session-reserve" {
			account.SessionReserve = sessionReserve
		}
	})
	path := cfg.AccountsFilePath()
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
		// A switched-off account keeps its directory: binding it again
		// would make that account impossible to switch back on.
		if existing.Disabled && config.NormalizeClaudeConfigDir(existing.ClaudeConfigDir) == account.ClaudeConfigDir {
			fmt.Fprintf(stderr, "%v: switched-off account %q already reads Claude Code profile %s; switch it back on instead\n",
				config.ErrAccountConflict, existing.ID, config.DisplayClaudeConfigDir(account.ClaudeConfigDir))
			return 1
		}
	}
	next := append(append([]config.ManagedAccount(nil), saved...), account)
	// The same checks the service applies at start: identity conflicts
	// (one directory bound twice), label rules, provider validity.
	if _, err := cfg.WithManagedAccounts(next); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	profile, err := nativeusage.InspectClaudeProfile(context.Background(), account.ClaudeConfigDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if profile.Fingerprint == "" {
		fmt.Fprintf(stderr, "%s is not signed in to a Claude account yet; sign in first with `CLAUDE_CONFIG_DIR=%s claude auth login`\n",
			config.DisplayClaudeConfigDir(profile.Dir), config.DisplayClaudeConfigDir(profile.Dir))
		return 1
	}
	if err := sameClaudeAccount(context.Background(), cfg, profile); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := map[string]any{"id": id, "label": account.Label, "claude_config_dir": config.DisplayClaudeConfigDir(account.ClaudeConfigDir),
		"plan": profile.Plan, "login_stored": profile.LoginStored, "accounts_file": path, "saved": !*dryRun}
	if !*dryRun {
		if err := config.WriteManagedAccounts(path, next); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *jsonOutput {
		writeJSON(stdout, result)
		return 0
	}
	verb := "added"
	if *dryRun {
		verb = "would add"
	}
	fmt.Fprintf(stdout, "%s account %s reading Claude Code profile %s (%s)\n", verb, id, config.DisplayClaudeConfigDir(account.ClaudeConfigDir), dash(profile.Plan))
	if !profile.LoginStored {
		fmt.Fprintf(stdout, "\nNo login is stored for this profile yet. Sign in with `CLAUDE_CONFIG_DIR=%s claude auth login`.\n",
			config.DisplayClaudeConfigDir(account.ClaudeConfigDir))
	}
	if !*dryRun {
		fmt.Fprintln(stdout, "\nRestart the Redline service to apply account changes.")
	}
	return 0
}

// sameClaudeAccount refuses binding a profile signed in to a Claude account
// Redline already watches, through the default login or another profile:
// the same numbers would be shown, and scheduled against, twice.
func sameClaudeAccount(ctx context.Context, cfg config.Config, profile nativeusage.ClaudeProfile) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if profile.Fingerprint == nativeusage.DefaultClaudeFingerprint(home) {
		for id, provider := range cfg.Providers {
			if strings.EqualFold(provider.Provider, "claude") && config.MeteringIdentity(provider) == "default:claude" {
				return fmt.Errorf("%w: %s is signed in to the same Claude account as the default login, which account %q already reads",
					config.ErrAccountConflict, config.DisplayClaudeConfigDir(profile.Dir), id)
			}
		}
	}
	dirs := map[string]string{}
	for id, provider := range cfg.Providers {
		if dir := config.NormalizeClaudeConfigDir(provider.ClaudeConfigDir); dir != "" {
			dirs[dir] = id
		}
	}
	if saved, readErr := config.ReadManagedAccounts(cfg.AccountsFilePath()); readErr == nil {
		for _, account := range saved {
			if dir := config.NormalizeClaudeConfigDir(account.ClaudeConfigDir); dir != "" {
				dirs[dir] = account.ID
			}
		}
	}
	for dir, id := range dirs {
		if dir == profile.Dir {
			continue
		}
		other, inspectErr := nativeusage.InspectClaudeProfile(ctx, dir)
		if inspectErr == nil && other.Fingerprint != "" && other.Fingerprint == profile.Fingerprint {
			return fmt.Errorf("%w: %s is signed in to the same Claude account as %s, which account %q already reads",
				config.ErrAccountConflict, config.DisplayClaudeConfigDir(profile.Dir), config.DisplayClaudeConfigDir(dir), id)
		}
	}
	return nil
}

func sanitizeID(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

// friendlyLabel turns a directory suffix into a display name: "alpha" ->
// "Alpha", "client-a" -> "Client A".
func friendlyLabel(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for index, word := range words {
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
