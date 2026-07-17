package harness_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/harness"
	redprocess "github.com/jfox/redline/internal/process"
)

func TestCodexAdapterBuildsNoninteractiveCommand(t *testing.T) {
	runner := &captureRunner{}
	adapter := harness.Adapter{Runner: runner}
	result, err := adapter.Run(context.Background(), harness.Request{
		RunID: "run-1", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "add tests"},
		Profile: domain.ExecutionProfile{
			HarnessType: "codex-cli", Model: "gpt-5.3-codex", HarnessArgs: []string{"--strict-config"},
		},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "exec --json --color never -C /tmp/work -m gpt-5.3-codex --strict-config -"
	if runner.command.Name != "codex" || strings.Join(runner.command.Args, " ") != want || runner.stdin != "add tests" {
		t.Fatalf("command=%#v stdin=%q", runner.command, runner.stdin)
	}
	if result.ExitCode != 0 || !strings.HasSuffix(result.OutputFile, "run-1.stdout.jsonl") {
		t.Fatalf("result = %#v", result)
	}
}

func TestClaudeAdapterBuildsNoninteractiveCommand(t *testing.T) {
	runner := &captureRunner{}
	adapter := harness.Adapter{Runner: runner}
	_, err := adapter.Run(context.Background(), harness.Request{
		RunID: "run-2", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "review auth"},
		Profile: domain.ExecutionProfile{
			HarnessType: "claude-code", Model: "sonnet", HarnessArgs: []string{"--effort", "high"},
		},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "--print --output-format stream-json --verbose --permission-mode auto --model sonnet --effort high"
	if runner.command.Name != "claude" || strings.Join(runner.command.Args, " ") != want ||
		runner.command.Dir != "/tmp/work" || runner.stdin != "review auth" {
		t.Fatalf("command=%#v stdin=%q", runner.command, runner.stdin)
	}
}

func TestHarnessResultUsesAbsoluteArtifactPaths(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	outputDirectory, err := filepath.Rel(cwd, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (harness.Adapter{Runner: &captureRunner{}}).Run(context.Background(), harness.Request{
		RunID: "run-relative", OutputDirectory: outputDirectory,
		Task:      domain.Task{ID: "task", Prompt: "do work"},
		Profile:   domain.ExecutionProfile{HarnessType: "command", HarnessCommand: "agent --run"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(result.OutputFile) || !filepath.IsAbs(result.ErrorFile) {
		t.Fatalf("artifact paths are not absolute: %#v", result)
	}
}

func TestGenericCommandHarnessReceivesPromptAndEnvironment(t *testing.T) {
	runner := &captureRunner{}
	adapter := harness.Adapter{Runner: runner}
	_, err := adapter.Run(context.Background(), harness.Request{
		RunID: "run-3", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Name: "Review", Prompt: "do work"},
		Profile:   domain.ExecutionProfile{HarnessType: "command", HarnessCommand: "agent --run"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.command.Name != "/bin/sh" || strings.Join(runner.command.Args, " ") != "-lc agent --run" || runner.stdin != "do work" {
		t.Fatalf("command=%#v stdin=%q", runner.command, runner.stdin)
	}
	if !contains(runner.command.Env, "REDLINE_RUN_ID=run-3") {
		t.Fatalf("environment = %#v", runner.command.Env)
	}
}

func TestPromptFileIsReadRelativeToWorkspace(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "task.md"), []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &captureRunner{}
	adapter := harness.Adapter{Runner: runner}
	_, err := adapter.Run(context.Background(), harness.Request{
		RunID: "run-4", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", PromptFile: "task.md"},
		Profile:   domain.ExecutionProfile{HarnessType: "codex-cli"},
		Workspace: domain.Workspace{Directory: workspaceDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.stdin != "from file" {
		t.Fatalf("stdin = %q", runner.stdin)
	}
}

type captureRunner struct {
	command redprocess.Command
	stdin   string
}

func (r *captureRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	if command.Stdin != nil {
		data, _ := io.ReadAll(command.Stdin)
		r.stdin = string(data)
	}
	if command.Stdout != nil {
		_, _ = io.WriteString(command.Stdout, `{"type":"result"}`)
	}
	return 0, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
