package harness

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The Anthropic strip runs with a goos parameter, so the Windows
// case-insensitive match is exercised on every OS.
func TestAnthropicCredentialsStripMatchesCaseOnlyOnWindows(t *testing.T) {
	inherited := []string{"Anthropic_Api_Key=inherited", "anthropic_oauth_token=x", "Claude_Code_Use_Bedrock=1",
		"claude_code_oauth_token=x", "Aws_Bearer_Token_Bedrock=x", "KEEP=yes"}
	windows := anthropicCredentialEnvironment("windows", inherited, "sk-ant-api03-SYNTHETIC")
	if !slices.Equal(windows, []string{"KEEP=yes", "ANTHROPIC_API_KEY=sk-ant-api03-SYNTHETIC"}) {
		t.Errorf("windows = %v", windows)
	}
	linux := anthropicCredentialEnvironment("linux", inherited, "sk-ant-api03-SYNTHETIC")
	if !slices.Equal(linux, append(slices.Clone(inherited), "ANTHROPIC_API_KEY=sk-ant-api03-SYNTHETIC")) {
		t.Errorf("linux = %v", linux)
	}
	exact := anthropicCredentialEnvironment("linux", []string{"ANTHROPIC_API_KEY=old", "ANTHROPIC_OAUTH_TOKEN=x",
		"CLAUDE_CODE_GATEWAY_TOKEN=x", "AWS_BEARER_TOKEN_BEDROCK=x", "AWS_REGION=us-east-1"}, "sk-ant-api03-SYNTHETIC")
	if !slices.Equal(exact, []string{"AWS_REGION=us-east-1", "ANTHROPIC_API_KEY=sk-ant-api03-SYNTHETIC"}) {
		t.Errorf("linux exact = %v", exact)
	}
}

// Scratch directories a crashed service left behind are removed at the next
// start once they are a day old; recent ones (a run still going) and
// unrelated directories stay.
func TestSweepRunScratchRemovesOnlyStaleRunDirectories(t *testing.T) {
	temp := t.TempDir()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	mkScratch := func(name string, age time.Duration) string {
		path := filepath.Join(temp, name)
		if err := os.MkdirAll(filepath.Join(path, "projects"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return path
	}
	staleClaude := mkScratch("redline-claude-credits-123", 25*time.Hour)
	stalePi := mkScratch("redline-pi-credits-456", 48*time.Hour)
	fresh := mkScratch("redline-claude-credits-789", time.Hour)
	other := mkScratch("redline-other-1", 72*time.Hour)
	if err := os.WriteFile(filepath.Join(temp, "redline-pi-credits-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(temp, "redline-pi-credits-file"), now.Add(-72*time.Hour), now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, errs := sweepRunScratch(temp, now)
	if len(errs) != 0 || removed != 2 {
		t.Fatalf("removed %d, errors %v", removed, errs)
	}
	for _, gone := range []string{staleClaude, stalePi} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived: %v", gone, err)
		}
	}
	for _, kept := range []string{fresh, other, filepath.Join(temp, "redline-pi-credits-file")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}
