package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

type Manager struct{ Runner redprocess.Runner }

type FinalizeRequest struct {
	RunID      string
	TaskID     string
	Status     string
	ExitCode   int
	OutputFile string
	Profile    domain.ExecutionProfile
	Workspace  domain.Workspace
}

type CleanupRequest struct {
	Success   bool
	Profile   domain.ExecutionProfile
	Workspace domain.Workspace
}

func (m Manager) Prepare(
	ctx context.Context,
	runID string,
	taskName string,
	profile domain.ExecutionProfile,
) (domain.Workspace, error) {
	if profile.Repository == "" {
		return domain.Workspace{}, fmt.Errorf("workspace repository is required")
	}
	if info, err := os.Stat(profile.Repository); err != nil || !info.IsDir() {
		return domain.Workspace{}, fmt.Errorf("workspace repository is not a directory: %s", profile.Repository)
	}
	var prepared domain.Workspace
	var err error
	switch profile.WorkspaceProvider {
	case "existing-directory":
		if profile.RequireClean {
			if err := m.requireClean(ctx, profile.Repository); err != nil {
				return domain.Workspace{}, err
			}
		}
		prepared = domain.Workspace{Directory: profile.Repository}
	case "git-worktree":
		prepared, err = m.prepareGitWorktree(ctx, runID, profile)
	case "devx":
		prepared, err = m.prepareDevX(ctx, runID, profile)
	case "command":
		return m.prepareCommand(ctx, runID, taskName, profile)
	default:
		return domain.Workspace{}, fmt.Errorf("unsupported workspace provider %q", profile.WorkspaceProvider)
	}
	if err != nil {
		return domain.Workspace{}, err
	}
	if profile.PrepareCommand != "" {
		if err := m.runSetupHook(ctx, runID, taskName, profile, prepared); err != nil {
			return prepared, err
		}
	}
	return prepared, nil
}

func (m Manager) Finalize(ctx context.Context, request FinalizeRequest) error {
	if request.Profile.FinalizeCommand == "" {
		return nil
	}
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-lc", request.Profile.FinalizeCommand},
		Dir: request.Workspace.Directory, Env: hookEnvironment(request),
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return fmt.Errorf("run finalize hook: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("finalize hook exited with code %d", exitCode)
	}
	return nil
}

func (m Manager) Cleanup(ctx context.Context, request CleanupRequest) error {
	policy := request.Profile.CleanupPolicy
	if policy == "" || policy == "never" || (policy == "on_success" && !request.Success) {
		return nil
	}
	if policy != "always" && policy != "on_success" {
		return fmt.Errorf("unsupported cleanup policy %q", policy)
	}
	var command redprocess.Command
	switch request.Profile.WorkspaceProvider {
	case "devx":
		if request.Workspace.SessionID == "" {
			return fmt.Errorf("cannot clean DevX workspace without session id")
		}
		command = redprocess.Command{
			Name: "devx", Args: []string{"session", "rm", request.Workspace.SessionID, "--force"},
			Dir: request.Profile.Repository, Stdout: io.Discard, Stderr: io.Discard,
		}
	case "git-worktree":
		root := filepath.Join(request.Profile.Repository, ".redline", "worktrees")
		relative, err := filepath.Rel(root, request.Workspace.Directory)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			return fmt.Errorf("refusing to clean workspace outside managed worktree root")
		}
		command = redprocess.Command{
			Name: "git", Args: []string{"-C", request.Profile.Repository, "worktree", "remove", "--force", request.Workspace.Directory},
			Stdout: io.Discard, Stderr: io.Discard,
		}
	case "existing-directory", "command":
		return nil
	default:
		return fmt.Errorf("unsupported workspace provider %q", request.Profile.WorkspaceProvider)
	}
	exitCode, err := m.runner().Run(ctx, command)
	if err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("workspace cleanup exited with code %d", exitCode)
	}
	return nil
}

func (m Manager) runSetupHook(
	ctx context.Context,
	runID string,
	taskName string,
	profile domain.ExecutionProfile,
	prepared domain.Workspace,
) error {
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-lc", profile.PrepareCommand}, Dir: prepared.Directory,
		Env: append(os.Environ(),
			"REDLINE_RUN_ID="+runID,
			"REDLINE_TASK_NAME="+taskName,
			"REDLINE_REPO="+profile.Repository,
			"REDLINE_BASE_BRANCH="+profile.BaseBranch,
			"REDLINE_WORKSPACE_DIR="+prepared.Directory,
			"REDLINE_SESSION_ID="+prepared.SessionID,
		),
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return fmt.Errorf("run workspace setup hook: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("workspace setup hook exited with code %d", exitCode)
	}
	return nil
}

func (m Manager) prepareGitWorktree(
	ctx context.Context,
	runID string,
	profile domain.ExecutionProfile,
) (domain.Workspace, error) {
	name := safeName(runID)
	directory := filepath.Join(profile.Repository, ".redline", "worktrees", name)
	if err := os.MkdirAll(filepath.Dir(directory), 0o755); err != nil {
		return domain.Workspace{}, fmt.Errorf("create worktree parent: %w", err)
	}
	branch := "redline/" + name
	base := profile.BaseBranch
	if base == "" {
		base = "HEAD"
	}
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", profile.Repository, "worktree", "add", "-b", branch, directory, base},
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("create git worktree: %w", err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("git worktree creation exited with code %d", exitCode)
	}
	if err := requireDirectory(directory); err != nil {
		return domain.Workspace{}, err
	}
	return domain.Workspace{Directory: directory, Branch: branch}, nil
}

func (m Manager) prepareDevX(
	ctx context.Context,
	runID string,
	profile domain.ExecutionProfile,
) (domain.Workspace, error) {
	session := "redline-" + safeName(runID)
	args := []string{"session", "create", session, "--no-tmux"}
	args = append(args, profile.WorkspaceArgs...)
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "devx", Args: args,
		Dir: profile.Repository, Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("create DevX session: %w", err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("DevX session creation exited with code %d", exitCode)
	}
	directory := filepath.Join(profile.Repository, ".worktrees", session)
	if err := requireDirectory(directory); err != nil {
		return domain.Workspace{}, err
	}
	return domain.Workspace{Directory: directory, SessionID: session}, nil
}

func (m Manager) prepareCommand(
	ctx context.Context,
	runID string,
	taskName string,
	profile domain.ExecutionProfile,
) (domain.Workspace, error) {
	if profile.PrepareCommand == "" {
		return domain.Workspace{}, fmt.Errorf("command workspace requires prepare_command")
	}
	var stdout, stderr bytes.Buffer
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-lc", profile.PrepareCommand}, Dir: profile.Repository,
		Env: append(os.Environ(),
			"REDLINE_RUN_ID="+runID,
			"REDLINE_TASK_NAME="+taskName,
			"REDLINE_REPO="+profile.Repository,
			"REDLINE_BASE_BRANCH="+profile.BaseBranch,
		),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("prepare command workspace: %w", err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("prepare command exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		WorkingDirectory string            `json:"working_directory"`
		Branch           string            `json:"branch"`
		SessionID        string            `json:"session_id"`
		Metadata         map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return domain.Workspace{}, fmt.Errorf("decode prepare command output: %w", err)
	}
	if err := requireDirectory(result.WorkingDirectory); err != nil {
		return domain.Workspace{}, err
	}
	return domain.Workspace{
		Directory: result.WorkingDirectory, Branch: result.Branch,
		SessionID: result.SessionID, Metadata: result.Metadata,
	}, nil
}

func (m Manager) requireClean(ctx context.Context, repository string) error {
	var stdout bytes.Buffer
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", repository, "status", "--porcelain"},
		Stdout: &stdout, Stderr: io.Discard,
	})
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("git status exited with code %d", exitCode)
	}
	if strings.TrimSpace(stdout.String()) != "" {
		return fmt.Errorf("existing workspace has uncommitted changes")
	}
	return nil
}

func (m Manager) runner() redprocess.Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return redprocess.ExecRunner{}
}

func hookEnvironment(request FinalizeRequest) []string {
	return append(os.Environ(),
		"REDLINE_RUN_ID="+request.RunID,
		"REDLINE_TASK_ID="+request.TaskID,
		"REDLINE_RUN_STATUS="+request.Status,
		"REDLINE_EXIT_CODE="+strconv.Itoa(request.ExitCode),
		"REDLINE_WORKSPACE_DIR="+request.Workspace.Directory,
		"REDLINE_SESSION_ID="+request.Workspace.SessionID,
		"REDLINE_OUTPUT_FILE="+request.OutputFile,
	)
}

func requireDirectory(path string) error {
	if path == "" {
		return fmt.Errorf("workspace command did not return working_directory")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("prepared workspace is not a directory: %s", path)
	}
	return nil
}

func safeName(value string) string {
	clean := strings.Trim(unsafeName.ReplaceAllString(value, "-"), "-")
	if clean == "" {
		return "run"
	}
	return clean
}
