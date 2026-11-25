package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
	"github.com/croutoncreations/redline/internal/config"
)

const profileServiceConfig = `
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
    pace_thresholds:
      - time_remaining: 72h
        min_weekly_remaining: 0.50
`

// profileHome builds a synthetic home folder: a default login and Claude
// Code profiles whose metadata carries personal details Redline must not
// repeat. No real keychain items exist for these paths.
func profileHome(t *testing.T) (home, configPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	write := func(dir, uuid string) {
		_ = os.MkdirAll(dir, 0o700)
		metadata := `{"oauthAccount":{"accountUuid":"` + uuid + `","emailAddress":"someone@example.invalid","displayName":"Some One",
			"organizationName":"Synthetic Org","organizationType":"claude_team","organizationRole":"user","userRateLimitTier":"default_claude_max_5x"}}`
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(metadata), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "uuid-default")
	write(filepath.Join(home, ".claude-cc-kilo"), "uuid-kilo")
	write(filepath.Join(home, ".claude-cc-bravo"), "uuid-bravo")
	write(filepath.Join(home, ".claude-cc-copy"), "uuid-kilo")    // same account as kilo
	write(filepath.Join(home, ".claude-cc-mine"), "uuid-default") // same account as the default login
	_ = os.MkdirAll(filepath.Join(home, ".claude-cc-empty"), 0o700)
	configPath = filepath.Join(home, "redline.yaml")
	if err := os.WriteFile(configPath, []byte(profileServiceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, configPath
}

func TestAccountProfilesListsDistinctAccountsWithoutPersonalDetails(t *testing.T) {
	_, configPath := profileHome(t)
	var stdout, stderr bytes.Buffer
	if exit := cli.Run([]string{"--config", configPath, "account", "profiles", "--json"}, &stdout, &stderr, time.Now); exit != 0 {
		t.Fatalf("exit %d: %s", exit, stderr.String())
	}
	var rows []struct {
		Display, Plan, Fingerprint string
		Suggested                  string `json:"suggested_id"`
		Label                      string `json:"suggested_label"`
		SameAs                     string `json:"same_as"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Display] = row.Suggested + "|" + row.Label + "|" + row.Plan + "|" + row.SameAs
	}
	want := map[string]string{
		"~/.claude-cc-copy":  "claude-copy|Copy|Team · Max 5x|",
		"~/.claude-cc-kilo":  "claude-kilo|Kilo|Team · Max 5x|~/.claude-cc-copy",
		"~/.claude-cc-mine":  "claude-mine|Mine|Team · Max 5x|default login",
		"~/.claude-cc-bravo": "claude-bravo|Bravo|Team · Max 5x|",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v (the metadata-less directory is not a profile)", got)
	}
	for dir, value := range want {
		if got[dir] != value {
			t.Errorf("%s = %q, want %q", dir, got[dir], value)
		}
	}
	for _, leak := range []string{"example.invalid", "Some One", "Synthetic Org", "uuid-"} {
		if strings.Contains(stdout.String(), leak) {
			t.Fatalf("output carries %q:\n%s", leak, stdout.String())
		}
	}
}

func TestAddProfileWritesOnlyThePathAndRefusesDuplicateAccounts(t *testing.T) {
	home, configPath := profileHome(t)
	accountsPath := filepath.Join(home, "accounts.yaml")
	run := func(args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		exit := cli.Run(append([]string{"--config", configPath, "account", "add-profile"}, args...), &stdout, &stderr, time.Now)
		return exit, stdout.String() + stderr.String()
	}
	if exit, out := run("claude-kilo", "--dir", "~/.claude-cc-kilo", "--label", "Kilo", "--dry-run"); exit != 0 || !strings.Contains(out, "would add") {
		t.Fatalf("dry run: %d %s", exit, out)
	}
	if _, err := os.Stat(accountsPath); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote the accounts file")
	}
	if exit, out := run("claude-kilo", "--dir", "~/.claude-cc-kilo", "--label", "Kilo"); exit != 0 || !strings.Contains(out, "Restart") {
		t.Fatalf("add: %d %s", exit, out)
	}
	saved, err := config.ReadManagedAccounts(accountsPath)
	if err != nil || len(saved) != 1 || saved[0].ClaudeConfigDir != filepath.Join(home, ".claude-cc-kilo") ||
		saved[0].UsageSource != "native" || saved[0].Label != "Kilo" {
		t.Fatalf("saved = %#v err=%v", saved, err)
	}
	refusals := map[string][]string{
		"same Claude account as ~/.claude-cc-kilo": {"claude-copy", "--dir", "~/.claude-cc-copy"},
		"same Claude account as the default login": {"claude-mine", "--dir", "~/.claude-cc-mine"},
		"both read Claude Code profile":            {"claude-alpha2", "--dir", "~/.claude-cc-kilo/"},
		"already exists":                           {"claude-kilo", "--dir", "~/.claude-cc-bravo"},
		"not a Claude Code profile":                {"claude-empty", "--dir", "~/.claude-cc-empty"},
		"must not contain '@'":                     {"claude-bravo", "--dir", "~/.claude-cc-bravo", "--label", "s@example.invalid"},
		"default Claude Code profile":              {"claude-default", "--dir", "~/.claude"},
	}
	before, _ := os.ReadFile(accountsPath)
	for want, args := range refusals {
		if exit, out := run(args...); exit == 0 || !strings.Contains(out, want) {
			t.Errorf("%v: exit %d, want %q in %s", args, exit, want, out)
		}
	}
	after, _ := os.ReadFile(accountsPath)
	if !bytes.Equal(before, after) {
		t.Fatal("a refused profile changed the accounts file")
	}
}

func TestAddProfileRefusesADirectoryASwitchedOffAccountReads(t *testing.T) {
	home, configPath := profileHome(t)
	if err := config.WriteManagedAccounts(filepath.Join(home, "accounts.yaml"), []config.ManagedAccount{{ID: "claude-old", Provider: "claude",
		UsageSource: "native", ClaudeConfigDir: filepath.Join(home, ".claude-cc-kilo"), Disabled: true}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--config", configPath, "account", "add-profile", "claude-kilo", "--dir", "~/.claude-cc-kilo"}, &stdout, &stderr, time.Now)
	if exit == 0 || !strings.Contains(stderr.String(), "switched-off account \"claude-old\"") {
		t.Fatalf("exit %d: %s", exit, stderr.String())
	}
}
