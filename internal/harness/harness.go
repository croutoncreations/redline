package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
)

type Adapter struct{ Runner redprocess.Runner }

type Request struct {
	RunID           string
	OutputDirectory string
	Task            domain.Task
	Profile         domain.ExecutionProfile
	Workspace       domain.Workspace
}

type Result struct {
	ExitCode   int    `json:"exit_code"`
	OutputFile string `json:"output_file"`
	ErrorFile  string `json:"error_file"`
}

func (a Adapter) Run(ctx context.Context, request Request) (Result, error) {
	prompt, err := loadPrompt(request.Task, request.Workspace.Directory)
	if err != nil {
		return Result{}, err
	}
	if request.RunID == "" || request.OutputDirectory == "" || request.Workspace.Directory == "" {
		return Result{}, fmt.Errorf("run id, output directory, and workspace directory are required")
	}
	outputDirectory, err := filepath.Abs(request.OutputDirectory)
	if err != nil {
		return Result{}, fmt.Errorf("resolve run output directory: %w", err)
	}
	if err := os.MkdirAll(outputDirectory, 0o700); err != nil {
		return Result{}, fmt.Errorf("create run output directory: %w", err)
	}
	result := Result{
		OutputFile: filepath.Join(outputDirectory, request.RunID+".stdout.jsonl"),
		ErrorFile:  filepath.Join(outputDirectory, request.RunID+".stderr.log"),
	}
	stdout, err := os.OpenFile(result.OutputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{}, fmt.Errorf("create harness output: %w", err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(result.ErrorFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{}, fmt.Errorf("create harness error output: %w", err)
	}
	defer stderr.Close()

	command, err := buildCommand(request, prompt, stdout, stderr)
	if err != nil {
		return Result{}, err
	}
	exitCode, err := a.runner().Run(ctx, command)
	result.ExitCode = exitCode
	if err != nil {
		return result, fmt.Errorf("launch %s harness: %w", request.Profile.HarnessType, err)
	}
	return result, nil
}

func buildCommand(
	request Request,
	prompt string,
	stdout, stderr io.Writer,
) (redprocess.Command, error) {
	base := redprocess.Command{
		Dir: request.Workspace.Directory, Stdout: stdout, Stderr: stderr,
	}
	switch request.Profile.HarnessType {
	case "codex-cli":
		base.Name = "codex"
		base.Args = []string{"exec", "--json", "--color", "never", "-C", request.Workspace.Directory}
		if request.Profile.Model != "" && request.Profile.Model != "default" {
			base.Args = append(base.Args, "-m", request.Profile.Model)
		}
		base.Args = append(base.Args, request.Profile.HarnessArgs...)
		base.Args = append(base.Args, "-")
		base.Stdin = strings.NewReader(prompt)
	case "claude-code":
		base.Name = "claude"
		base.Args = []string{"--print", "--output-format", "stream-json", "--verbose", "--permission-mode", "auto"}
		if request.Profile.Model != "" && request.Profile.Model != "default" {
			base.Args = append(base.Args, "--model", request.Profile.Model)
		}
		base.Args = append(base.Args, request.Profile.HarnessArgs...)
		base.Stdin = strings.NewReader(prompt)
	case "command":
		if request.Profile.HarnessCommand == "" {
			return redprocess.Command{}, fmt.Errorf("command harness requires harness_command")
		}
		base.Name = "/bin/sh"
		base.Args = []string{"-lc", request.Profile.HarnessCommand}
		base.Stdin = strings.NewReader(prompt)
		base.Env = append(os.Environ(),
			"REDLINE_RUN_ID="+request.RunID,
			"REDLINE_TASK_ID="+request.Task.ID,
			"REDLINE_TASK_NAME="+request.Task.Name,
			"REDLINE_WORKSPACE_DIR="+request.Workspace.Directory,
			"REDLINE_SESSION_ID="+request.Workspace.SessionID,
		)
	default:
		return redprocess.Command{}, fmt.Errorf("unsupported harness %q", request.Profile.HarnessType)
	}
	return base, nil
}

func loadPrompt(task domain.Task, workspaceDirectory string) (string, error) {
	if task.Prompt != "" {
		return task.Prompt, nil
	}
	if task.PromptFile == "" {
		return "", fmt.Errorf("task requires prompt or prompt_file")
	}
	path := task.PromptFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceDirectory, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read task prompt: %w", err)
	}
	return string(data), nil
}

func (a Adapter) runner() redprocess.Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return redprocess.ExecRunner{}
}
