package workspace_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/workspace"
)

func TestUsageLimitHookRunsInWorkspaceWithPromptAndContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	dir := t.TempDir()
	artifacts := t.TempDir()
	manager := workspace.Manager{OutputDirectory: artifacts}
	request := workspace.UsageLimitRequest{
		RunID: "run-1", TaskID: "task-1", TaskName: "Fix bug", Prompt: "fix the bug",
		HarnessSessionID: "sess-1", LimitType: "five_hour", ResetsAt: "2026-11-14T15:00:00Z",
		Profile: domain.ExecutionProfile{
			HarnessType: "claude-code",
			OnUsageLimitCommand: `cat > handoff-prompt.txt; ` +
				`printf '%s|%s|%s|%s|%s' "$REDLINE_HARNESS" "$REDLINE_HARNESS_SESSION_ID" "$REDLINE_USAGE_LIMIT_TYPE" "$REDLINE_USAGE_LIMIT_RESETS_AT" "$PWD" > handoff-env.txt; ` +
				`echo took over`,
		},
		Workspace: domain.Workspace{Directory: dir},
	}
	if err := manager.RunUsageLimitHook(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "handoff-prompt.txt"))
	env, _ := os.ReadFile(filepath.Join(dir, "handoff-env.txt"))
	resolved, _ := filepath.EvalSymlinks(dir)
	if string(prompt) != "fix the bug" {
		t.Fatalf("prompt = %q", prompt)
	}
	parts := strings.Split(string(env), "|")
	if len(parts) != 5 || parts[0] != "claude-code" || parts[1] != "sess-1" || parts[2] != "five_hour" ||
		parts[3] != "2026-11-14T15:00:00Z" {
		t.Fatalf("env = %q", env)
	}
	if got, _ := filepath.EvalSymlinks(parts[4]); got != resolved {
		t.Fatalf("hook ran in %q, want %q", parts[4], resolved)
	}
	stdout, _ := os.ReadFile(workspace.ArtifactPath(artifacts, "run-1", "usage_limit", "stdout"))
	if strings.TrimSpace(string(stdout)) != "took over" {
		t.Fatalf("captured stdout = %q", stdout)
	}

	request.Profile.OnUsageLimitCommand = "exit 3"
	if err := manager.RunUsageLimitHook(t.Context(), request); err == nil || !strings.Contains(err.Error(), "code 3") {
		t.Fatalf("err = %v", err)
	}
}
