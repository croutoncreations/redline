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

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/workspace"
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

func TestGitWorktreeWorkspaceFailureIncludesGitStderr(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		_, _ = io.WriteString(command.Stderr, "fatal: branch 'redline/run-1' already exists\n")
		return 128, nil
	}}
	manager := workspace.Manager{Runner: runner}
	_, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "git-worktree", Repository: repo, BaseBranch: "main",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"fatal: branch 'redline/run-1' already exists", "code 128", repo, "redline/run-1", "main"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
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

func TestDevXWorkspaceFailureIncludesDevXStderr(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		_, _ = io.WriteString(command.Stderr, "error: authentication expired, run `devx login`\n")
		return 1, nil
	}}
	manager := workspace.Manager{Runner: runner}
	_, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "devx", Repository: repo,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"authentication expired", "code 1", "redline-run-1", repo} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestExistingDirectoryRequireCleanReportsDirtyFiles(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		_, _ = io.WriteString(command.Stdout, " M internal/workspace/manager.go\n?? scratch.txt\n")
		return 0, nil
	}}
	manager := workspace.Manager{Runner: runner}
	_, err := manager.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "existing-directory", Repository: repo, RequireClean: true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{repo, "internal/workspace/manager.go", "scratch.txt", "require_clean"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestCommandWorkspaceConsumesJSONContract(t *testing.T) {
	repo := t.TempDir()
	created := filepath.Join(repo, "custom")
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		wantName, wantArgs := redprocess.ShellCommand("prepare-workspace")
		if command.Name != wantName || strings.Join(command.Args, " ") != strings.Join(wantArgs, " ") {
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
		ResultFile: "/tmp/run-1.result.json",
		Profile:    domain.ExecutionProfile{FinalizeCommand: "finalize-workspace"},
		Workspace:  domain.Workspace{Directory: "/tmp/work", SessionID: "session-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REDLINE_RUN_ID=run-1", "REDLINE_TASK_ID=task-1", "REDLINE_WORKSPACE_DIR=/tmp/work", "REDLINE_RUN_STATUS=completed", "REDLINE_RESULT_FILE=/tmp/run-1.result.json"} {
		if !strings.Contains(environment, want) {
			t.Errorf("environment missing %q: %s", want, environment)
		}
	}
}

func TestPrepareAndFinalizeHooksCaptureOutput(t *testing.T) {
	repo := t.TempDir()
	artifacts := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if strings.Contains(strings.Join(command.Args, " "), "setup-workspace") {
			_, _ = io.WriteString(command.Stdout, "prepare output\n")
			_, _ = io.WriteString(command.Stderr, "prepare warning\n")
			return 0, nil
		}
		_, _ = io.WriteString(command.Stdout, "finalize output\n")
		_, _ = io.WriteString(command.Stderr, "finalize warning\n")
		return 0, nil
	}}
	manager := workspace.Manager{Runner: runner, OutputDirectory: artifacts}
	profile := domain.ExecutionProfile{
		WorkspaceProvider: "existing-directory", Repository: repo,
		PrepareCommand: "setup-workspace", FinalizeCommand: "finalize-workspace",
	}
	prepared, err := manager.Prepare(context.Background(), "run-logs", "Task", profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Finalize(context.Background(), workspace.FinalizeRequest{
		RunID: "run-logs", TaskID: "task", Status: "completed", Profile: profile, Workspace: prepared,
	}); err != nil {
		t.Fatal(err)
	}

	wants := map[string]string{
		"prepare.stdout.log":  "prepare output\n",
		"prepare.stderr.log":  "prepare warning\n",
		"finalize.stdout.log": "finalize output\n",
		"finalize.stderr.log": "finalize warning\n",
	}
	for suffix, want := range wants {
		path := filepath.Join(artifacts, "run-logs."+suffix)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %o, want 0600 (prepare/finalize hooks run with the full process environment and must not leave world-readable logs)", path, mode)
		}
	}
}

func TestBuiltInWorkspaceRunsPrepareHookInsideWorkspace(t *testing.T) {
	repo := t.TempDir()
	called := false
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		called = true
		wantName, wantArgs := redprocess.ShellCommand("setup-workspace")
		if command.Name != wantName || command.Dir != repo || strings.Join(command.Args, " ") != strings.Join(wantArgs, " ") {
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

func TestDevXCleanupFailureIncludesDevXStderr(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		_, _ = io.WriteString(command.Stderr, "error: session redline-run-1 not found\n")
		return 1, nil
	}}
	manager := workspace.Manager{Runner: runner}
	err := manager.Cleanup(context.Background(), workspace.CleanupRequest{
		Success:   true,
		Profile:   domain.ExecutionProfile{WorkspaceProvider: "devx", Repository: repo, CleanupPolicy: "on_success"},
		Workspace: domain.Workspace{Directory: filepath.Join(repo, ".worktrees", "redline-run-1"), SessionID: "redline-run-1"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"session redline-run-1 not found", "code 1", "devx", filepath.Join(repo, ".worktrees", "redline-run-1")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestCleanupPoliciesControlWorkspaceRemoval(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		success bool
		wantRun bool
	}{
		{name: "never after success", policy: "never", success: true},
		{name: "never after failure", policy: "never", success: false},
		{name: "on success after success", policy: "on_success", success: true, wantRun: true},
		{name: "on success after failure", policy: "on_success", success: false},
		{name: "always after success", policy: "always", success: true, wantRun: true},
		{name: "always after failure", policy: "always", success: false, wantRun: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			runner := &fakeRunner{run: func(redprocess.Command) (int, error) {
				called = true
				return 0, nil
			}}
			manager := workspace.Manager{Runner: runner}
			err := manager.Cleanup(context.Background(), workspace.CleanupRequest{
				Success: test.success,
				Profile: domain.ExecutionProfile{
					WorkspaceProvider: "devx", Repository: t.TempDir(), CleanupPolicy: test.policy,
				},
				Workspace: domain.Workspace{SessionID: "redline-run-1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if called != test.wantRun {
				t.Fatalf("cleanup command called = %v, want %v", called, test.wantRun)
			}
		})
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

func assertErrorContains(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestPrepareRepositoryProblemsNameThePathAndCause(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := workspace.Manager{}
	profile := func(repository string) domain.ExecutionProfile {
		return domain.ExecutionProfile{WorkspaceProvider: "git-worktree", Repository: repository}
	}

	_, err := manager.Prepare(context.Background(), "run-1", "Task", profile(missing))
	assertErrorContains(t, err, `workspace_provider "git-worktree"`, "cannot stat "+missing, "no such file or directory")

	_, err = manager.Prepare(context.Background(), "run-1", "Task", profile(file))
	assertErrorContains(t, err, file+" exists but is not a directory")

	_, err = manager.Prepare(context.Background(), "run-1", "Task", profile(""))
	assertErrorContains(t, err, `workspace_provider "git-worktree" requires repository`)
}

func TestPrepareRejectsUnknownProviderListingValidOnes(t *testing.T) {
	_, err := workspace.Manager{}.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "docker", Repository: t.TempDir(),
	})
	assertErrorContains(t, err, `"docker"`, "existing-directory", "git-worktree", "devx", "command", "runtime-owned")
}

func TestSetupHookFailureNamesDirectoryAndLogFiles(t *testing.T) {
	repo := t.TempDir()
	artifacts := t.TempDir()
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		_, _ = io.WriteString(command.Stderr, "npm ERR! secret-token-value\n")
		return 7, nil
	}}
	manager := workspace.Manager{Runner: runner, OutputDirectory: artifacts}
	_, err := manager.Prepare(context.Background(), "run-9", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "existing-directory", Repository: repo, PrepareCommand: "npm ci",
	})
	assertErrorContains(t, err, "exited with code 7", repo, "run run-9",
		filepath.Join(artifacts, "run-9.prepare.stderr.log"))
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Errorf("hook stderr must stay in the 0600 log file, not the error: %q", err)
	}
}

func TestFinalizeHookFailureNamesWorkspaceRunAndLogFiles(t *testing.T) {
	artifacts := t.TempDir()
	runner := &fakeRunner{run: func(redprocess.Command) (int, error) { return 3, nil }}
	manager := workspace.Manager{Runner: runner, OutputDirectory: artifacts}
	err := manager.Finalize(context.Background(), workspace.FinalizeRequest{
		RunID: "run-4", Status: "failed",
		Profile:   domain.ExecutionProfile{FinalizeCommand: "push-branch"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	assertErrorContains(t, err, "exited with code 3", "/tmp/work", "run run-4", "status failed",
		filepath.Join(artifacts, "run-4.finalize.stderr.log"))

	err = workspace.Manager{Runner: runner}.Finalize(context.Background(), workspace.FinalizeRequest{
		RunID:     "run-4",
		Profile:   domain.ExecutionProfile{FinalizeCommand: "push-branch"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	assertErrorContains(t, err, "hook output was not captured")
}

func TestCommandWorkspaceBadOutputDescribesButDoesNotEchoOutput(t *testing.T) {
	repo := t.TempDir()
	prepare := func(output string, exit int) error {
		runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
			_, _ = io.WriteString(command.Stdout, output)
			return exit, nil
		}}
		_, err := workspace.Manager{Runner: runner}.Prepare(context.Background(), "run-1", "Task", domain.ExecutionProfile{
			WorkspaceProvider: "command", Repository: repo, PrepareCommand: "prepare-workspace",
		})
		return err
	}

	assertErrorContains(t, prepare("Cloning into 'x'...\n", 0), "working_directory", "got 19 bytes of output")
	if err := prepare(`token=hunter2-secret`, 0); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("malformed prepare output must not be echoed into the stored error: %v", err)
	}
	assertErrorContains(t, prepare("", 0), "empty output")
	assertErrorContains(t, prepare(`{"branch":"b"}`, 0), "unusable working_directory", "did not return working_directory")
	missing := filepath.Join(repo, "nope")
	assertErrorContains(t, prepare(`{"working_directory":"`+missing+`"}`, 0), "cannot stat "+missing)
	assertErrorContains(t, prepare("", 2), "code 2", repo)
}

func TestCleanupErrorsNameWorkspaceAndValidValues(t *testing.T) {
	manager := workspace.Manager{}
	err := manager.Cleanup(context.Background(), workspace.CleanupRequest{
		Profile: domain.ExecutionProfile{CleanupPolicy: "sometimes", WorkspaceProvider: "devx"},
	})
	assertErrorContains(t, err, `"sometimes"`, "never", "on_success", "always")

	err = manager.Cleanup(context.Background(), workspace.CleanupRequest{
		Profile:   domain.ExecutionProfile{CleanupPolicy: "always", WorkspaceProvider: "devx"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	assertErrorContains(t, err, "/tmp/work", "session id")
}
