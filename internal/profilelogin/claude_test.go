package profilelogin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	// A proxy in the developer's own environment would (rightly) bring the
	// CA bundle along; this test is about the variables without one.
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	for name, value := range map[string]string{
		"CLAUDE_CONFIG_DIR": "/somewhere/else", "ANTHROPIC_API_KEY": "sk-x", "CLAUDE_CODE_OAUTH_TOKEN": "t",
		"ANTHROPIC_BASE_URL": "https://proxy", "ANTHROPIC_AUTH_TOKEN": "a", "CLAUDE_CODE_USE_BEDROCK": "1",
		"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_MODEL": "opus", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "opus",
		"CLAUDE_CODE_CUSTOM_OAUTH_URL": "https://evil", "GITHUB_TOKEN": "ghp_x", "REDLINE_API_TOKEN": "rl",
		"NODE_EXTRA_CA_CERTS": "/ca.pem",
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
	if !strings.Contains(got, "GATEPOST_HOST_DISABLE=1\n") {
		t.Fatal("a capture shim must not proxy the renewal")
	}
	if strings.Count(got, "CLAUDE_CONFIG_DIR=") != 1 || !strings.Contains(got, "CLAUDE_CONFIG_DIR=/Users/x/.claude-cc-frank\n") {
		t.Fatalf("profile dir not set alone: %s", got)
	}
	for _, leaked := range []string{"sk-x", "ANTHROPIC_", "CLAUDE_CODE_", "GITHUB_TOKEN", "REDLINE_API_TOKEN", "NODE_EXTRA_CA_CERTS"} {
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

func TestProfileEnvironmentKeepsOnlyTheBasics(t *testing.T) {
	env := ProfileEnvironment([]string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/old", "ANTHROPIC_API_KEY=k", "HOME=/h", "LC_ALL=C", "HTTPS_PROXY=http://p", "AWS_SECRET_ACCESS_KEY=s"}, "/new")
	if strings.Join(env, ",") != "PATH=/bin,HOME=/h,LC_ALL=C,HTTPS_PROXY=http://p,GATEPOST_HOST_DISABLE=1,CLAUDE_CONFIG_DIR=/new" {
		t.Fatalf("env = %v", env)
	}
}

// Under go test, the real claude is never started.
func TestTheRealClaudeNeverRunsInTests(t *testing.T) {
	err := (ClaudeRenewer{}).Renew(context.Background(), "/p", ModeStatus)
	if err == nil || !strings.Contains(err.Error(), "go test") {
		t.Fatalf("err = %v", err)
	}
}

// A run that outlives its timeout is stopped with its whole process group,
// and Renew returns promptly even if a child kept the output open.
func TestAStuckClaudeIsStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 600 &\nsleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	// The caller's deadline does not cut a renewal short (it might be
	// saving a new token); the run's own timeout does. Use a short one.
	err := (ClaudeRenewer{Binary: binary, timeout: 300 * time.Millisecond}).Renew(ctx, "/p", ModeStatus)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("Renew took %s", elapsed)
	}
}

// A proxy's certificate authority comes along only with the proxy.
func TestCertificateAuthorityOnlyWithAProxy(t *testing.T) {
	without := ProfileEnvironment([]string{"NODE_EXTRA_CA_CERTS=/ca.pem"}, "/p")
	with := ProfileEnvironment([]string{"HTTPS_PROXY=http://p", "NODE_EXTRA_CA_CERTS=/ca.pem"}, "/p")
	if strings.Contains(strings.Join(without, ","), "ca.pem") || !strings.Contains(strings.Join(with, ","), "NODE_EXTRA_CA_CERTS=/ca.pem") {
		t.Fatalf("without = %v, with = %v", without, with)
	}
}

// Each run gets its own private directory, removed afterwards, so another
// user on a shared /tmp cannot block renewals.
func TestEachRenewalRunsInAFreshPrivateDirectory(t *testing.T) {
	binary, record := fakeClaude(t, 0)
	t.Setenv("TMPDIR", t.TempDir())
	// Another user's leftover at the old fixed name changes nothing.
	if err := os.Mkdir(filepath.Join(os.TempDir(), "redline-profile-login"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (ClaudeRenewer{Binary: binary}).Renew(context.Background(), "/p", ModeStatus); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	work := lines[len(lines)-1]
	if !strings.Contains(filepath.Base(work), "redline-profile-login-") {
		t.Fatalf("ran in %s", work)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("work directory %s was left behind", work)
	}
}
