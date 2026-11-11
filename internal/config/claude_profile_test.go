package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestClaudeConfigDirIsOneSpellingAndItsOwnIdentity(t *testing.T) {
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude-cc-alpha")
	for _, spelling := range []string{"~/.claude-cc-alpha", "~/.claude-cc-alpha/", want + "/", " " + want + " ", want + "/./"} {
		if got := config.NormalizeClaudeConfigDir(spelling); got != want {
			t.Fatalf("%q -> %q, want %q", spelling, got, want)
		}
	}
	a := config.MeteringIdentity(config.Provider{Provider: "claude", ClaudeConfigDir: "~/.claude-cc-alpha"})
	b := config.MeteringIdentity(config.Provider{Provider: "claude", ClaudeConfigDir: want + "/"})
	other := config.MeteringIdentity(config.Provider{Provider: "claude", ClaudeConfigDir: "~/.claude-cc-bravo"})
	def := config.MeteringIdentity(config.Provider{Provider: "claude"})
	if a != b || a == other || a == def {
		t.Fatalf("identities: a=%q b=%q other=%q default=%q", a, b, other, def)
	}
	if got := config.DisplayClaudeConfigDir(want); got != "~/.claude-cc-alpha" {
		t.Fatalf("display = %q", got)
	}
}

func TestClaudeConfigDirDefaultsToNativeAndRefusesOtherSources(t *testing.T) {
	if got := (config.Provider{Provider: "claude", ClaudeConfigDir: "~/.claude-cc-a"}).EffectiveUsageSource(); got != "native" {
		t.Fatalf("effective source = %q, want native: auto would read OpenUsage's default card, another account", got)
	}
	base := writeConfig(t, claudeConfig)
	cfg, err := config.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]config.ManagedAccount{
		"usage_source must be native": {ID: "c-a", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-a", UsageSource: "openusage"},
		"auto":                        {ID: "c-b", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-b", UsageSource: "auto"},
		"two different sources":       {ID: "c-c", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-c", OpenUsageCard: "claude@ab12cd34"},
		"only for claude":             {ID: "c-d", Provider: "codex", ClaudeConfigDir: "~/.claude-cc-d"},
		"absolute path":               {ID: "c-e", Provider: "claude", ClaudeConfigDir: "relative/dir"},
		"default Claude Code profile": {ID: "c-f", Provider: "claude", ClaudeConfigDir: "~/.claude"},
	}
	for want, account := range cases {
		if _, err := cfg.WithManagedAccounts([]config.ManagedAccount{account}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: err = %v, want it to mention %q", account, err, want)
		}
	}
	if _, err := cfg.WithManagedAccounts([]config.ManagedAccount{{ID: "c-ok", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-ok", UsageSource: "native"}}); err != nil {
		t.Fatalf("a native profile account should load: %v", err)
	}
}

func TestOneProfileDirectoryBoundTwiceIsAConflict(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, claudeConfig))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.WithManagedAccounts([]config.ManagedAccount{
		{ID: "claude-alpha", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-alpha"},
		{ID: "claude-alpha2", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-alpha/"},
	})
	if !errors.Is(err, config.ErrAccountConflict) || !strings.Contains(err.Error(), "Claude Code profile") {
		t.Fatalf("err = %v", err)
	}
	// Five profiles beside the default login are five separate accounts.
	var five []config.ManagedAccount
	for _, name := range []string{"charlie", "delta", "echo", "alpha", "bravo"} {
		five = append(five, config.ManagedAccount{ID: "claude-" + name, Provider: "claude", Label: strings.ToUpper(name[:1]) + name[1:],
			UsageSource: "native", ClaudeConfigDir: "~/.claude-cc-" + name})
	}
	next, err := cfg.WithManagedAccounts(five)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.AccountsByProvider()["claude"]) != 6 {
		t.Fatalf("claude accounts = %v", next.AccountsByProvider()["claude"])
	}
	// The default login keeps its legacy history: profiles are not
	// default-login accounts, so they do not make its owner ambiguous...
	// except that ownership is by sole account, which now there is not.
	if owners := next.LegacyUsageOwners()["claude"]; len(owners) != 0 {
		t.Fatalf("legacy owners = %v", owners)
	}
}

func TestAccountsFileKeepsOnlyTheProfilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := config.WriteManagedAccounts(path, []config.ManagedAccount{{ID: "claude-alpha", Provider: "claude", Label: "Alpha",
		UsageSource: "native", ClaudeConfigDir: "/Users/x/.claude-cc-alpha"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "claude_config_dir: /Users/x/.claude-cc-alpha") {
		t.Fatalf("file:\n%s", text)
	}
	for _, forbidden := range []string{"token", "password", "secret", "oauth", "email"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("accounts file mentions %q:\n%s", forbidden, text)
		}
	}
	read, err := config.ReadManagedAccounts(path)
	if err != nil || len(read) != 1 || read[0].ClaudeConfigDir != "/Users/x/.claude-cc-alpha" {
		t.Fatalf("read back %#v err=%v", read, err)
	}
}

func TestProfileConflictMessageShowsNoHomePath(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, claudeConfig))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	_, err = cfg.WithManagedAccounts([]config.ManagedAccount{
		{ID: "claude-a", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-a"},
		{ID: "claude-b", Provider: "claude", ClaudeConfigDir: filepath.Join(home, ".claude-cc-a")},
	})
	if err == nil || strings.Contains(err.Error(), home) || !strings.Contains(err.Error(), "~/.claude-cc-a") {
		t.Fatalf("err = %v", err)
	}
}

// A card id is an opaque hash and a profile a directory: nothing says they
// are different people, so the two sources are never mixed for Claude.
func TestOpenUsageCardsAndProfilesAreNeverMixed(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, claudeConfig))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.WithManagedAccounts([]config.ManagedAccount{
		{ID: "claude-card", Provider: "claude", OpenUsageCard: "claude@ab12cd34"},
		{ID: "claude-alpha", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-alpha"},
	})
	if !errors.Is(err, config.ErrAccountConflict) || !strings.Contains(err.Error(), "one source") {
		t.Fatalf("err = %v", err)
	}
	for _, accounts := range [][]config.ManagedAccount{
		{{ID: "c1", Provider: "claude", OpenUsageCard: "claude@ab12cd34"}, {ID: "c2", Provider: "claude", OpenUsageCard: "claude@cd34ef56"}},
		{{ID: "p1", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-a"}, {ID: "p2", Provider: "claude", ClaudeConfigDir: "~/.claude-cc-b"}},
	} {
		if _, err := cfg.WithManagedAccounts(accounts); err != nil {
			t.Fatalf("one source should load: %v", err)
		}
	}
}
