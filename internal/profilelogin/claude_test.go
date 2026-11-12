package profilelogin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeClaude is a shell script that records its arguments and environment.
func fakeClaude(t *testing.T, exit int) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := "#!/bin/sh\n{ echo \"args:$*\"; env; pwd; } > '" + record + "'\nexit " + string(rune('0'+exit)) + "\n"
	binary := filepath.Join(dir, "claude")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, record
}

func TestRenewRunsClaudeOnTheProfileOnly(t *testing.T) {
	binary, record := fakeClaude(t, 0)
	for name, value := range map[string]string{
		"CLAUDE_CONFIG_DIR": "/somewhere/else", "ANTHROPIC_API_KEY": "sk-x", "CLAUDE_CODE_OAUTH_TOKEN": "t",
		"ANTHROPIC_BASE_URL": "https://proxy", "ANTHROPIC_AUTH_TOKEN": "a",
	} {
		t.Setenv(name, value)
	}
	if err := (ClaudeRenewer{Binary: binary}).Renew(context.Background(), "/Users/x/.claude-cc-frank", ModeStatus); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	got := string(raw)
	if !strings.Contains(got, "args:auth status --json\n") {
		t.Fatalf("args: %s", got)
	}
	if strings.Count(got, "CLAUDE_CONFIG_DIR=") != 1 || !strings.Contains(got, "CLAUDE_CONFIG_DIR=/Users/x/.claude-cc-frank\n") {
		t.Fatalf("profile dir not set alone: %s", got)
	}
	for _, leaked := range []string{"sk-x", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%s reached Claude Code", leaked)
		}
	}
}

// The prompt is the cheapest real request: haiku, no tools, no MCP, no
// skills or settings, nothing saved.
func TestPromptModeIsMinimal(t *testing.T) {
	binary, record := fakeClaude(t, 0)
	if err := (ClaudeRenewer{Binary: binary}).Renew(context.Background(), "/p", ModePrompt); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	args := strings.SplitN(string(raw), "\n", 2)[0]
	for _, want := range []string{"--print", "--model haiku", "--tools ", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--setting-sources "} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "--bare") {
		t.Fatal("--bare changes how Claude Code authenticates")
	}
}

func TestRenewReportsAFailingRun(t *testing.T) {
	binary, _ := fakeClaude(t, 1)
	if err := (ClaudeRenewer{Binary: binary}).Renew(context.Background(), "/p", ModeStatus); err == nil {
		t.Fatal("exit 1 must be an error")
	}
}

func TestProfileEnvironmentReplacesInheritedProfile(t *testing.T) {
	env := ProfileEnvironment([]string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/old", "ANTHROPIC_API_KEY=k", "HOME=/h"}, "/new")
	if strings.Join(env, ",") != "PATH=/bin,HOME=/h,CLAUDE_CONFIG_DIR=/new" {
		t.Fatalf("env = %v", env)
	}
}
