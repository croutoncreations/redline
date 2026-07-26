package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/hermes"
	redprocess "github.com/jfox/redline/internal/process"
)

type ContextStore interface {
	GetAgentContext(context.Context, string) (domain.AgentContext, error)
	GetRuntimeConnection(context.Context, string) (domain.RuntimeConnection, error)
}

type HermesRunner interface {
	Run(context.Context, hermes.RunRequest) (hermes.RunResult, error)
	TriggerJob(context.Context, domain.RuntimeConnection, string) (hermes.Job, error)
}

type Adapter struct {
	Runner   redprocess.Runner
	Contexts ContextStore
	Hermes   HermesRunner
}

type Request struct {
	RunID             string
	OutputDirectory   string
	Task              domain.Task
	Profile           domain.ExecutionProfile
	Workspace         domain.Workspace
	OnExternalStarted func(domain.ExternalRun) error
}

type Result struct {
	ExitCode   int            `json:"exit_code"`
	OutputFile string         `json:"output_file"`
	ErrorFile  string         `json:"error_file"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func (a Adapter) Run(ctx context.Context, request Request) (Result, error) {
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

	if request.Profile.HarnessType == "hermes" {
		if request.Task.RuntimeJobID != "" {
			return a.runHermesJob(ctx, request, stdout, result)
		}
		prompt, err := loadPrompt(request.Task, request.Workspace.Directory)
		if err != nil {
			return Result{}, err
		}
		return a.runHermes(ctx, request, prompt, stdout, result)
	}
	prompt, err := loadPrompt(request.Task, request.Workspace.Directory)
	if err != nil {
		return Result{}, err
	}
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

func (a Adapter) runHermesJob(
	ctx context.Context,
	request Request,
	stdout io.Writer,
	result Result,
) (Result, error) {
	if a.Contexts == nil {
		return result, fmt.Errorf("Hermes harness requires an agent context store")
	}
	if a.Hermes == nil {
		a.Hermes = hermes.Client{}
	}
	agentContext, err := a.Contexts.GetAgentContext(ctx, request.Profile.AgentContextID)
	if err != nil {
		return result, fmt.Errorf("load Hermes agent context: %w", err)
	}
	connection, err := a.Contexts.GetRuntimeConnection(ctx, agentContext.RuntimeConnectionID)
	if err != nil {
		return result, fmt.Errorf("load Hermes runtime connection: %w", err)
	}
	job, err := a.Hermes.TriggerJob(ctx, connection, request.Task.RuntimeJobID)
	if err != nil {
		result.ExitCode = 1
		return result, fmt.Errorf("trigger Hermes job: %w", err)
	}
	if request.OnExternalStarted != nil {
		if err := request.OnExternalStarted(domain.ExternalRun{
			RuntimeConnectionID: connection.ID, RunID: job.ID,
		}); err != nil {
			return result, fmt.Errorf("record Hermes job: %w", err)
		}
	}
	if err := json.NewEncoder(stdout).Encode(map[string]any{
		"type": "hermes.job_triggered", "job_id": job.ID, "name": job.Name,
		"model": job.Model, "provider": job.Provider,
	}); err != nil {
		return result, fmt.Errorf("write Hermes job result: %w", err)
	}
	result.ExitCode = 0
	result.Metadata = map[string]any{
		"external_job_id": job.ID, "actual_model": job.Model, "actual_provider": job.Provider,
	}
	return result, nil
}

func (a Adapter) runHermes(
	ctx context.Context,
	request Request,
	prompt string,
	stdout io.Writer,
	result Result,
) (Result, error) {
	if a.Contexts == nil {
		return result, fmt.Errorf("Hermes harness requires an agent context store")
	}
	if a.Hermes == nil {
		a.Hermes = hermes.Client{}
	}
	agentContext, err := a.Contexts.GetAgentContext(ctx, request.Profile.AgentContextID)
	if err != nil {
		return result, fmt.Errorf("load Hermes agent context: %w", err)
	}
	connection, err := a.Contexts.GetRuntimeConnection(ctx, agentContext.RuntimeConnectionID)
	if err != nil {
		return result, fmt.Errorf("load Hermes runtime connection: %w", err)
	}
	provider, model := splitHermesModel(request.Profile.Model)
	runResult, err := a.Hermes.Run(ctx, hermes.RunRequest{
		RunID: request.RunID, Prompt: prompt, Connection: connection, Context: agentContext,
		Model: model, Provider: provider, OnExternalStarted: request.OnExternalStarted,
	})
	if err != nil {
		result.ExitCode = 1
		return result, fmt.Errorf("run Hermes agent: %w", err)
	}
	record := map[string]any{
		"type": "hermes.result", "session_id": runResult.SessionID, "output": runResult.Output,
		"usage": runResult.Usage, "model": runResult.Model, "provider": runResult.Provider,
	}
	if err := json.NewEncoder(stdout).Encode(record); err != nil {
		return result, fmt.Errorf("write Hermes result: %w", err)
	}
	result.ExitCode = 0
	result.Metadata = map[string]any{
		"external_session_id": runResult.SessionID, "usage": runResult.Usage,
		"actual_model": runResult.Model, "actual_provider": runResult.Provider,
	}
	return result, nil
}

func splitHermesModel(value string) (provider, model string) {
	value = strings.TrimSpace(value)
	if value == "" || value == "default" {
		return "", ""
	}
	if before, after, found := strings.Cut(value, "/"); found {
		return before, after
	}
	return "", value
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
		base.Args = append(base.Args, "--append-system-prompt", claudeWorkspaceBoundary(request.Workspace.Directory))
		base.Stdin = strings.NewReader(prompt)
	case "pi":
		base.Name = "pi"
		base.Args = []string{"--print", "--mode", "json", "--name", "redline-" + request.RunID}
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

func claudeWorkspaceBoundary(directory string) string {
	return fmt.Sprintf(
		`Your exact workspace directory is %q. Perform all task work inside that directory. `+
			`Resolve every relative path from that directory, and use that directory—not another checkout—as the project root. `+
			`Do not read or modify a parent checkout or another worktree.`,
		directory,
	)
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
