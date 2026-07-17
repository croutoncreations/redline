package workspace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
	"github.com/jfox/redline/internal/workspace"
)

func TestExistingDirectoryWorkspace(t *testing.T) {
	repo := t.TempDir()
	manager := workspace.Manager{}
	got, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "existing-directory", Repository: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Directory != repo {
		t.Fatalf("directory = %q", got.Directory)
	}
}

func TestGitWorktreeWorkspaceUsesIsolatedBranch(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if command.Name != "git" || strings.Join(command.Args, " ") != "-C "+repo+" worktree add -b redline/run-1 "+filepath.Join(repo, ".redline", "worktrees", "run-1")+" main" {
			t.Fatalf("command = %#v", command)
		}
		return 0, os.MkdirAll(filepath.Join(repo, ".redline", "worktrees", "run-1"), 0o755)
	}}
	manager := workspace.Manager{Runner: runner}
	got, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "git-worktree", Repository: repo, BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "redline/run-1" || got.Directory != filepath.Join(repo, ".redline", "worktrees", "run-1") {
		t.Fatalf("workspace = %#v", got)
	}
}

func TestDevXWorkspaceCreatesNamedSession(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if command.Name != "devx" || strings.Join(command.Args, " ") != "session create redline-run-1 --no-tmux" || command.Dir != repo {
			t.Fatalf("command = %#v", command)
		}
		return 0, os.MkdirAll(filepath.Join(repo, ".worktrees", "redline-run-1"), 0o755)
	}}
	manager := workspace.Manager{Runner: runner}
	got, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "devx", Repository: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "redline-run-1" || got.Directory != filepath.Join(repo, ".worktrees", "redline-run-1") {
		t.Fatalf("workspace = %#v", got)
	}
}

func TestDevXWorkspacePassesConfiguredArguments(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if got := strings.Join(command.Args, " "); got != "session create redline-run-1 --no-tmux --target host" {
			t.Fatalf("args = %q", got)
		}
		return 0, os.MkdirAll(filepath.Join(repo, ".worktrees", "redline-run-1"), 0o755)
	}}
	manager := workspace.Manager{Runner: runner}
	_, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "devx", Repository: repo, WorkspaceArgs: []string{"--target", "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommandWorkspaceConsumesJSONContract(t *testing.T) {
	repo := t.TempDir()
	created := filepath.Join(repo, "custom")
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if command.Name != "/bin/sh" || strings.Join(command.Args, " ") != "-lc prepare-workspace" {
			t.Fatalf("command = %#v", command)
		}
		_ = os.MkdirAll(created, 0o755)
		return 0, json.NewEncoder(command.Stdout).Encode(map[string]string{
			"working_directory": created, "session_id": "custom-session",
		})
	}}
	manager := workspace.Manager{Runner: runner}
	got, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "command", Repository: repo, PrepareCommand: "prepare-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Directory != created || got.SessionID != "custom-session" {
		t.Fatalf("workspace = %#v", got)
	}
}

func TestFinalizeHookReceivesRunEnvironment(t *testing.T) {
	var environment string
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		environment = strings.Join(command.Env, "\n")
		return 0, nil
	}}
	manager := workspace.Manager{Runner: runner}
	err := manager.Finalize(context.Background(), workspace.FinalizeRequest{
		RunID: "run-1", TaskID: "task-1", Status: "completed", ExitCode: 0,
		Profile:   domain.ExecutionProfile{FinalizeCommand: "finalize-workspace"},
		Workspace: domain.Workspace{Directory: "/tmp/work", SessionID: "session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REDLINE_RUN_ID=run-1", "REDLINE_TASK_ID=task-1", "REDLINE_WORKSPACE_DIR=/tmp/work", "REDLINE_RUN_STATUS=completed"} {
		if !strings.Contains(environment, want) {
			t.Errorf("environment missing %q: %s", want, environment)
		}
	}
}

func TestBuiltInWorkspaceRunsPrepareHookInsideWorkspace(t *testing.T) {
	repo := t.TempDir()
	called := false
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		called = true
		if command.Name != "/bin/sh" || command.Dir != repo || strings.Join(command.Args, " ") != "-lc setup-workspace" {
			t.Fatalf("command = %#v", command)
		}
		return 0, nil
	}}
	manager := workspace.Manager{Runner: runner}
	_, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "existing-directory", Repository: repo, PrepareCommand: "setup-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("prepare hook was not called")
	}
}

func TestDevXCleanupPolicyRemovesSession(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if command.Name != "devx" || strings.Join(command.Args, " ") != "session rm redline-run-1 --force" {
			t.Fatalf("command = %#v", command)
		}
		return 0, nil
	}}
	manager := workspace.Manager{Runner: runner}
	err := manager.Cleanup(context.Background(), workspace.CleanupRequest{
		Success:   true,
		Profile:   domain.ExecutionProfile{WorkspaceProvider: "devx", Repository: repo, CleanupPolicy: "on_success"},
		Workspace: domain.Workspace{Directory: filepath.Join(repo, ".worktrees", "redline-run-1"), SessionID: "redline-run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type fakeRunner struct {
	run func(redprocess.Command) (int, error)
}

func (f *fakeRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	if command.Stdout == nil {
		command.Stdout = io.Discard
	}
	if command.Stderr == nil {
		command.Stderr = &bytes.Buffer{}
	}
	return f.run(command)
}
