package harness_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

const syntheticClaudeAPIKey = "sk-ant-api03-SYNTHETIC-ACCOUNT-KEY"

func claudeCreditsProfile(harnessType string) domain.ExecutionProfile {
	return domain.ExecutionProfile{HarnessType: harnessType, HarnessCommand: "true", Model: "anthropic/claude-sonnet-4-6",
		ProviderAccountID: "claude-credits", APICreditsVendor: "anthropic", APICreditsKey: syntheticClaudeAPIKey}
}

// inspectingRunner records the command and, while the run is still going,
// what the run-scoped files look like.
type inspectingRunner struct {
	command    redprocess.Command
	configDir  string
	configMode os.FileMode
	configList []string
	extension  string
	extMode    os.FileMode
}

func (r *inspectingRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	if command.Stdin != nil {
		_, _ = io.ReadAll(command.Stdin)
	}
	if dirs := envValues(command.Env, "CLAUDE_CONFIG_DIR"); len(dirs) == 1 {
		r.configDir = dirs[0]
		if info, err := os.Stat(dirs[0]); err == nil {
			r.configMode = info.Mode().Perm()
		}
		entries, _ := os.ReadDir(dirs[0])
		for _, entry := range entries {
			r.configList = append(r.configList, entry.Name())
		}
	}
	if index := slices.Index(command.Args, "-e"); index >= 0 && index+1 < len(command.Args) {
		r.extension = command.Args[index+1]
		data, _ := os.ReadFile(r.extension)
		if info, err := os.Stat(r.extension); err == nil {
			r.extMode = info.Mode().Perm()
		}
		r.extension = r.extension + "\n" + string(data)
	}
	return 0, nil
}

func runClaudeCredits(t *testing.T, profile domain.ExecutionProfile) *inspectingRunner {
	t.Helper()
	runner := &inspectingRunner{}
	_, err := harness.Adapter{Runner: runner}.Run(context.Background(), harness.Request{
		RunID: "run-claude-credits", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "p"}, Profile: profile,
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

var anthropicRoutingVariables = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID",
	"ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_SERVICE_ACCOUNT_ID", "ANTHROPIC_WORKSPACE_ID"}

// A run on a Claude credits account spends exactly that account's key:
// every inherited Anthropic credential, routing and cloud switch is dropped
// first, then ANTHROPIC_API_KEY and the run marker are set.
func TestClaudeCreditsRunUsesOnlyTheAccountsKey(t *testing.T) {
	for _, name := range anthropicRoutingVariables {
		t.Setenv(name, "inherited-should-not-leak")
	}
	t.Setenv("REDLINE_ANTHROPIC_API_CREDITS", "inherited")
	t.Setenv("OPENAI_API_KEY", "sk-user-own")
	for _, harnessType := range []string{"claude-code", "pi", "command"} {
		env := runClaudeCredits(t, claudeCreditsProfile(harnessType)).command.Env
		if got := envValues(env, "ANTHROPIC_API_KEY"); len(got) != 1 || got[0] != syntheticClaudeAPIKey {
			t.Fatalf("%s: ANTHROPIC_API_KEY = %v", harnessType, got)
		}
		if got := envValues(env, "REDLINE_ANTHROPIC_API_CREDITS"); len(got) != 1 || got[0] != "1" {
			t.Fatalf("%s: REDLINE_ANTHROPIC_API_CREDITS = %v", harnessType, got)
		}
		for _, name := range anthropicRoutingVariables[1:] {
			if got := envValues(env, name); len(got) != 0 {
				t.Fatalf("%s: kept %s", harnessType, name)
			}
		}
		if got := envValues(env, "REDLINE_OPENAI_API_CREDITS"); len(got) != 0 {
			t.Fatalf("%s: marked as an OpenAI credits run", harnessType)
		}
		// The OpenAI environment is not this run's concern.
		if got := envValues(env, "OPENAI_API_KEY"); len(got) != 1 || got[0] != "sk-user-own" {
			t.Fatalf("%s: OPENAI_API_KEY = %v", harnessType, got)
		}
	}
}

// Windows environment names are case-insensitive: a mixed-case inherited
// credential is the same variable there and must be dropped too.
func TestClaudeCreditsRunDropsMixedCaseCredentialsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive environment names are Windows-only; WithoutVariables covers the matching")
	}
	t.Setenv("Anthropic_Api_Key", "inherited")
	env := runClaudeCredits(t, claudeCreditsProfile("command")).command.Env
	for _, entry := range env {
		if strings.EqualFold(strings.SplitN(entry, "=", 2)[0], "ANTHROPIC_API_KEY") && !strings.HasSuffix(entry, syntheticClaudeAPIKey) {
			t.Fatalf("kept %s", entry)
		}
	}
}

// Other runs, including subscription Claude Code runs, are never marked and
// keep their own Anthropic environment.
func TestOnlyClaudeCreditsRunsAreMarked(t *testing.T) {
	t.Setenv("REDLINE_ANTHROPIC_API_CREDITS", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-user-own")
	for _, profile := range []domain.ExecutionProfile{{HarnessType: "pi"}, {HarnessType: "command", HarnessCommand: "true"},
		{HarnessType: "pi", APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT"}} {
		env := runEnv(t, profile)
		if got := envValues(env, "REDLINE_ANTHROPIC_API_CREDITS"); len(got) != 0 {
			t.Fatalf("%s/%s run marked: %v", profile.HarnessType, profile.APICreditsVendor, got)
		}
		if got := envValues(env, "ANTHROPIC_API_KEY"); len(got) != 1 || got[0] != "sk-ant-user-own" {
			t.Fatalf("%s/%s ANTHROPIC_API_KEY = %v", profile.HarnessType, profile.APICreditsVendor, got)
		}
	}
}

// Claude Code prefers a stored subscription login over ANTHROPIC_API_KEY,
// so a credits run gets a fresh, empty, private CLAUDE_CONFIG_DIR of its
// own, removed when the run ends. The account's own profile directory, or
// one the service inherited, is never used.
func TestClaudeCreditsClaudeCodeRunsInAnEmptyPrivateConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/Users/someone/.claude-inherited")
	profile := claudeCreditsProfile("claude-code")
	profile.Model = "claude-sonnet-4-6"
	profile.ClaudeConfigDir = "/Users/someone/.claude-cc-alpha"
	runner := runClaudeCredits(t, profile)
	if dirs := envValues(runner.command.Env, "CLAUDE_CONFIG_DIR"); len(dirs) != 1 {
		t.Fatalf("CLAUDE_CONFIG_DIR = %v", dirs)
	}
	if runner.configDir == "" || strings.Contains(runner.configDir, ".claude") || !filepath.IsAbs(runner.configDir) {
		t.Fatalf("config dir = %q", runner.configDir)
	}
	if runtime.GOOS != "windows" && runner.configMode != 0o700 {
		t.Fatalf("config dir mode = %v", runner.configMode)
	}
	if len(runner.configList) != 0 {
		t.Fatalf("config dir was not empty: %v", runner.configList)
	}
	if _, err := os.Stat(runner.configDir); !os.IsNotExist(err) {
		t.Fatalf("config dir survived the run: %v", err)
	}
	if runner.command.Name != "claude" || !slices.Contains(runner.command.Args, "--print") {
		t.Fatalf("command = %s %v", runner.command.Name, runner.command.Args)
	}
	for _, arg := range runner.command.Args {
		if strings.Contains(arg, "sk-ant") {
			t.Fatal("the key is in argv")
		}
	}
}

// Two runs never share a config directory.
func TestClaudeCreditsConfigDirIsPerRun(t *testing.T) {
	first := runClaudeCredits(t, claudeCreditsProfile("claude-code")).configDir
	second := runClaudeCredits(t, claudeCreditsProfile("claude-code")).configDir
	if first == "" || first == second {
		t.Fatalf("config dirs %q and %q", first, second)
	}
}

// pi prefers a stored Anthropic OAuth login over ANTHROPIC_API_KEY, and
// --api-key would put the key in argv. A credits pi run is given an
// extension (-e, before the profile's own arguments) that moves anthropic
// models onto a provider reading $ANTHROPIC_API_KEY. The file holds no key.
func TestClaudeCreditsPiRunLoadsThePinExtension(t *testing.T) {
	profile := claudeCreditsProfile("pi")
	profile.HarnessArgs = []string{"--thinking", "high"}
	runner := runClaudeCredits(t, profile)
	args := runner.command.Args
	index := slices.Index(args, "-e")
	if index < 0 || index+2 >= len(args) || args[index+2] != "--thinking" {
		t.Fatalf("args = %v, want -e <path> before the profile's arguments", args)
	}
	if !slices.Contains(args, "anthropic/claude-sonnet-4-6") {
		t.Fatalf("args = %v", args)
	}
	path, contents, _ := strings.Cut(runner.extension, "\n")
	if !strings.Contains(contents, "redline-anthropic-credits") || !strings.Contains(contents, `"$ANTHROPIC_API_KEY"`) ||
		!strings.Contains(contents, "REDLINE_ANTHROPIC_API_CREDITS") {
		t.Fatalf("extension = %q", contents)
	}
	if strings.Contains(contents, "sk-ant") {
		t.Fatal("extension contains a key")
	}
	if runtime.GOOS != "windows" && runner.extMode != 0o600 {
		t.Fatalf("extension mode = %v", runner.extMode)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("extension survived the run: %v", err)
	}
	for _, arg := range args {
		if strings.Contains(arg, "sk-ant") {
			t.Fatal("the key is in argv")
		}
	}
}

// The extension is only for Claude credits pi runs.
func TestOtherPiRunsGetNoPinExtension(t *testing.T) {
	for _, profile := range []domain.ExecutionProfile{{HarnessType: "pi", Model: "anthropic/claude-sonnet-4-6"},
		{HarnessType: "pi", Model: "openai/gpt-5.6-sol", APICreditsVendor: "openai", APICreditsKey: "sk-proj-ACCOUNT"}} {
		if args := runClaudeCredits(t, profile).command.Args; slices.Contains(args, "-e") {
			t.Fatalf("%s run args = %v", profile.APICreditsVendor, args)
		}
	}
}

// The embedded extension is the one checked in: it names the provider and
// reads the key from the environment by reference only.
func TestEmbeddedPiExtensionReferencesTheKeyByName(t *testing.T) {
	source := harness.PiAnthropicCreditsExtension()
	for _, want := range []string{`"redline-anthropic-credits"`, `"$ANTHROPIC_API_KEY"`, `process.env.REDLINE_ANTHROPIC_API_CREDITS !== "1"`,
		`"https://api.anthropic.com"`, `"anthropic-messages"`, `"model_select"`, `"before_agent_start"`, `"anthropic-cli"`} {
		if !strings.Contains(source, want) {
			t.Errorf("extension lacks %s", want)
		}
	}
	if strings.Contains(source, "sk-ant") {
		t.Fatal("extension contains a literal key")
	}
}

// A rejected key surfaces as a sign-in failure that names the account's
// fix, not `claude auth login`, which would not help.
func TestClaudeCreditsSignedOutNamesTheAPIKeyFix(t *testing.T) {
	result, err := harness.Adapter{Runner: claudeSignedOutRunner{}}.Run(context.Background(), harness.Request{
		RunID: "run-signed-out", OutputDirectory: t.TempDir(), Task: domain.Task{ID: "task", Prompt: "p"},
		Profile: claudeCreditsProfile("claude-code"), Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !harness.IsAuthenticationFailure(result.Failure) || !strings.Contains(result.Failure, "set-api-key claude-credits") ||
		strings.Contains(result.Failure, "auth login") {
		t.Fatalf("failure = %q", result.Failure)
	}
}
