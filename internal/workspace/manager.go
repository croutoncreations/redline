package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

type Manager struct {
	Runner          redprocess.Runner
	OutputDirectory string
}

type FinalizeRequest struct {
	RunID      string
	TaskID     string
	Status     string
	ExitCode   int
	OutputFile string
	ResultFile string
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
	if profile.WorkspaceProvider == "runtime-owned" {
		if profile.Repository == "" {
			return domain.Workspace{}, fmt.Errorf("runtime-owned workspace requires repository to be set to the workspace directory, got empty value")
		}
		if profile.PrepareCommand != "" {
			return domain.Workspace{}, fmt.Errorf("runtime-owned workspace %s does not support prepare_command; remove prepare_command or use another workspace_provider", profile.Repository)
		}
		return domain.Workspace{Directory: profile.Repository}, nil
	}
	if profile.Repository == "" {
		return domain.Workspace{}, fmt.Errorf("workspace_provider %q requires repository to be set, got empty value", profile.WorkspaceProvider)
	}
	if err := statDirectory(profile.Repository); err != nil {
		return domain.Workspace{}, fmt.Errorf("workspace_provider %q repository: %w", profile.WorkspaceProvider, err)
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
		return domain.Workspace{}, fmt.Errorf("unsupported workspace_provider %q (want one of existing-directory, git-worktree, devx, command, runtime-owned)", profile.WorkspaceProvider)
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
	stdout, stderr, closeArtifacts, err := m.artifactWriters(request.RunID, "finalize")
	if err != nil {
		return err
	}
	defer closeArtifacts()
	logs := m.hookLogHint(request.RunID, "finalize")
	shellName, shellArgs := redprocess.ShellCommand(request.Profile.FinalizeCommand)
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: shellName, Args: shellArgs,
		Dir: request.Workspace.Directory, Env: hookEnvironment(request),
		Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return fmt.Errorf("run finalize hook in %s: %w", request.Workspace.Directory, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("finalize hook exited with code %d in %s (run %s, status %s)%s",
			exitCode, request.Workspace.Directory, request.RunID, request.Status, logs)
	}
	return nil
}

func (m Manager) Cleanup(ctx context.Context, request CleanupRequest) error {
	policy := request.Profile.CleanupPolicy
	if policy == "" || policy == "never" || (policy == "on_success" && !request.Success) {
		return nil
	}
	if policy != "always" && policy != "on_success" {
		return fmt.Errorf("unsupported cleanup_policy %q (want one of never, on_success, always)", policy)
	}
	var command redprocess.Command
	var stderr bytes.Buffer
	switch request.Profile.WorkspaceProvider {
	case "devx":
		if request.Workspace.SessionID == "" {
			return fmt.Errorf("cannot clean DevX workspace %s: run has no recorded DevX session id", request.Workspace.Directory)
		}
		command = redprocess.Command{
			Name: "devx", Args: []string{"session", "rm", request.Workspace.SessionID, "--force"},
			Dir: request.Profile.Repository, Stdout: io.Discard, Stderr: &stderr,
		}
	case "git-worktree":
		root := filepath.Join(request.Profile.Repository, ".redline", "worktrees")
		relative, err := filepath.Rel(root, request.Workspace.Directory)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			return fmt.Errorf("refusing to clean workspace %s: outside managed worktree root %s", request.Workspace.Directory, root)
		}
		command = redprocess.Command{
			Name: "git", Args: []string{"-C", request.Profile.Repository, "worktree", "remove", "--force", request.Workspace.Directory},
			Stdout: io.Discard, Stderr: &stderr,
		}
	case "existing-directory", "command", "runtime-owned":
		return nil
	default:
		return fmt.Errorf("cannot clean workspace %s: unsupported workspace_provider %q", request.Workspace.Directory, request.Profile.WorkspaceProvider)
	}
	exitCode, err := m.runner().Run(ctx, command)
	if err != nil {
		return fmt.Errorf("clean %s workspace %s: %w", request.Profile.WorkspaceProvider, request.Workspace.Directory, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("clean %s workspace %s exited with code %d: %s",
			request.Profile.WorkspaceProvider, request.Workspace.Directory, exitCode, strings.TrimSpace(stderr.String()))
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
	stdout, stderr, closeArtifacts, err := m.artifactWriters(runID, "prepare")
	if err != nil {
		return err
	}
	defer closeArtifacts()
	logs := m.hookLogHint(runID, "prepare")
	shellName, shellArgs := redprocess.ShellCommand(profile.PrepareCommand)
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: shellName, Args: shellArgs, Dir: prepared.Directory,
		Env: append(os.Environ(),
			"REDLINE_RUN_ID="+runID,
			"REDLINE_TASK_NAME="+taskName,
			"REDLINE_REPO="+profile.Repository,
			"REDLINE_BASE_BRANCH="+profile.BaseBranch,
			"REDLINE_WORKSPACE_DIR="+prepared.Directory,
			"REDLINE_SESSION_ID="+prepared.SessionID,
		),
		Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return fmt.Errorf("run workspace setup hook in %s: %w", prepared.Directory, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("workspace setup hook exited with code %d in %s (run %s)%s",
			exitCode, prepared.Directory, runID, logs)
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
		return domain.Workspace{}, fmt.Errorf("create worktree parent directory %s: %w", filepath.Dir(directory), err)
	}
	branch := "redline/" + name
	base := profile.BaseBranch
	if base == "" {
		base = "HEAD"
	}
	var stderr bytes.Buffer
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", profile.Repository, "worktree", "add", "-b", branch, directory, base},
		Stdout: io.Discard, Stderr: &stderr,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("create git worktree %s (branch %s, base %s) in %s: %w", directory, branch, base, profile.Repository, err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("git worktree add %s (branch %s, base %s) in %s exited with code %d: %s",
			directory, branch, base, profile.Repository, exitCode, strings.TrimSpace(stderr.String()))
	}
	if err := requireDirectory(directory); err != nil {
		return domain.Workspace{}, fmt.Errorf("git worktree add reported success for branch %s but: %w", branch, err)
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
	var stderr bytes.Buffer
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "devx", Args: args,
		Dir: profile.Repository, Stdout: io.Discard, Stderr: &stderr,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("create DevX session %s in %s: %w", session, profile.Repository, err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("devx session create %s in %s exited with code %d: %s",
			session, profile.Repository, exitCode, strings.TrimSpace(stderr.String()))
	}
	directory := filepath.Join(profile.Repository, ".worktrees", session)
	if err := requireDirectory(directory); err != nil {
		return domain.Workspace{}, fmt.Errorf("devx session create %s reported success but: %w", session, err)
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
		return domain.Workspace{}, fmt.Errorf("workspace_provider \"command\" requires prepare_command to be set, got empty value")
	}
	var stdout, stderr bytes.Buffer
	shellName, shellArgs := redprocess.ShellCommand(profile.PrepareCommand)
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: shellName, Args: shellArgs, Dir: profile.Repository,
		Env: append(os.Environ(),
			"REDLINE_RUN_ID="+runID,
			"REDLINE_TASK_NAME="+taskName,
			"REDLINE_REPO="+profile.Repository,
			"REDLINE_BASE_BRANCH="+profile.BaseBranch,
		),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("run prepare command in %s: %w", profile.Repository, err)
	}
	if exitCode != 0 {
		return domain.Workspace{}, fmt.Errorf("prepare command exited with code %d in %s (stderr %s; contents are not echoed because they may hold secrets)",
			exitCode, profile.Repository, describeOutput(stderr.String()))
	}
	var result struct {
		WorkingDirectory string            `json:"working_directory"`
		Branch           string            `json:"branch"`
		SessionID        string            `json:"session_id"`
		Metadata         map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return domain.Workspace{}, fmt.Errorf("decode prepare command output as JSON with working_directory (%s; contents are not echoed because they may hold secrets): %w",
			describeOutput(stdout.String()), err)
	}
	if err := requireDirectory(result.WorkingDirectory); err != nil {
		return domain.Workspace{}, fmt.Errorf("prepare command returned an unusable working_directory: %w", err)
	}
	return domain.Workspace{
		Directory: result.WorkingDirectory, Branch: result.Branch,
		SessionID: result.SessionID, Metadata: result.Metadata,
	}, nil
}

func (m Manager) requireClean(ctx context.Context, repository string) error {
	var stdout, stderr bytes.Buffer
	exitCode, err := m.runner().Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", repository, "status", "--porcelain"},
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("check working tree in %s: %w", repository, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("git status in %s exited with code %d: %s", repository, exitCode, strings.TrimSpace(stderr.String()))
	}
	if dirty := strings.TrimSpace(stdout.String()); dirty != "" {
		return fmt.Errorf("existing workspace %s has uncommitted changes, run with a clean working tree or disable require_clean:\n%s", repository, dirty)
	}
	return nil
}

func (m Manager) runner() redprocess.Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return redprocess.ExecRunner{}
}

func ArtifactPath(outputDirectory, runID, phase, stream string) string {
	return filepath.Join(outputDirectory, safeName(runID)+"."+phase+"."+stream+".log")
}

func (m Manager) artifactWriters(runID, phase string) (io.Writer, io.Writer, func(), error) {
	if m.OutputDirectory == "" {
		return io.Discard, io.Discard, func() {}, nil
	}
	if err := os.MkdirAll(m.OutputDirectory, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create lifecycle artifact directory %s: %w", m.OutputDirectory, err)
	}
	stdout, err := os.OpenFile(ArtifactPath(m.OutputDirectory, runID, phase, "stdout"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create %s stdout artifact (output directory %s): %w", phase, m.OutputDirectory, err)
	}
	stderr, err := os.OpenFile(ArtifactPath(m.OutputDirectory, runID, phase, "stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdout.Close()
		return nil, nil, nil, fmt.Errorf("create %s stderr artifact (output directory %s): %w", phase, m.OutputDirectory, err)
	}
	return stdout, stderr, func() {
		_ = stdout.Close()
		_ = stderr.Close()
	}, nil
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
		"REDLINE_RESULT_FILE="+request.ResultFile,
	)
}

func requireDirectory(path string) error {
	if path == "" {
		return fmt.Errorf("workspace command did not return working_directory")
	}
	if err := statDirectory(path); err != nil {
		return fmt.Errorf("prepared workspace: %w", err)
	}
	return nil
}

// statDirectory distinguishes a missing path, an unreadable path, and a path
// that exists but is not a directory, since each needs a different fix.
func statDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err // the message already names the path
		}
		return fmt.Errorf("cannot stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory (mode %s)", path, info.Mode())
	}
	return nil
}

// hookLogHint names the captured hook output files. Hook stderr is not copied
// into the error: hooks run with the full process environment and their logs
// are deliberately 0600, while errors are stored on the run and shown in the
// dashboard.
func (m Manager) hookLogHint(runID, phase string) string {
	if m.OutputDirectory == "" {
		return "; hook output was not captured because no output directory is configured"
	}
	return "; see " + ArtifactPath(m.OutputDirectory, runID, phase, "stderr") +
		" and " + ArtifactPath(m.OutputDirectory, runID, phase, "stdout")
}

func describeOutput(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "got empty output"
	}
	return "got " + strconv.Itoa(len(value)) + " bytes of output"
}

func safeName(value string) string {
	clean := strings.Trim(unsafeName.ReplaceAllString(value, "-"), "-")
	if clean == "" {
		return "run"
	}
	return clean
}
