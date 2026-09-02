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
	RunJob(context.Context, hermes.JobRunRequest) (hermes.JobRunResult, error)
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

func ResultFilePath(outputDirectory, runID string) string {
	return filepath.Join(outputDirectory, runID+".result.json")
}

type Result struct {
	ExitCode   int            `json:"exit_code"`
	OutputFile string         `json:"output_file"`
	ErrorFile  string         `json:"error_file"`
	Failure    string         `json:"failure,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func IsAuthenticationFailure(failure string) bool {
	return strings.HasPrefix(failure, "Claude Code is signed out.") ||
		strings.HasPrefix(failure, "Codex CLI is signed out.")
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
	if exitCode != 0 {
		_ = stdout.Sync()
		_ = stderr.Sync()
		result.Failure = diagnoseFailure(request.Profile.HarnessType, result.OutputFile, result.ErrorFile)
	}
	return result, nil
}

func diagnoseFailure(harnessType string, paths ...string) string {
	var evidence strings.Builder
	for _, path := range paths {
		data, err := readTail(path, 64*1024)
		if err == nil {
			evidence.Write(data)
			evidence.WriteByte('\n')
		}
	}
	normalized := strings.ToLower(evidence.String())
	switch harnessType {
	case "claude-code":
		if strings.Contains(normalized, "not logged in") ||
			strings.Contains(normalized, `"error":"authentication_failed"`) {
			return "Claude Code is signed out. Run `claude auth login`, then retry this job."
		}
	case "codex-cli":
		if strings.Contains(normalized, "not logged in") ||
			strings.Contains(normalized, "login required") {
			return "Codex CLI is signed out. Run `codex login`, then retry this job."
		}
	}
	return ""
}

func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(int64(0), info.Size()-limit)
	data := make([]byte, info.Size()-offset)
	_, err = file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}

func (a Adapter) runHermesJob(
	ctx context.Context,
	request Request,
	stdout io.Writer,
	result Result,
) (Result, error) {
	if a.Contexts == nil {
		return result, fmt.Errorf("hermes harness requires an agent context store")
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
	jobResult, err := a.Hermes.RunJob(ctx, hermes.JobRunRequest{
		Connection:        connection,
		JobID:             request.Task.RuntimeJobID,
		OnExternalStarted: request.OnExternalStarted,
	})
	if err != nil {
		result.ExitCode = 1
		if jobResult.Run.ID != "" {
			_ = json.NewEncoder(stdout).Encode(hermesJobRecord(jobResult, err.Error()))
			result.Metadata = hermesJobMetadata(jobResult)
		}
		return result, fmt.Errorf("run Hermes job: %w", err)
	}
	if err := json.NewEncoder(stdout).Encode(hermesJobRecord(jobResult, "")); err != nil {
		return result, fmt.Errorf("write Hermes job result: %w", err)
	}
	result.ExitCode = 0
	result.Metadata = hermesJobMetadata(jobResult)
	return result, nil
}

func hermesJobRecord(jobResult hermes.JobRunResult, errorMessage string) map[string]any {
	return map[string]any{
		"type": "hermes.result", "job_id": jobResult.Job.ID, "name": jobResult.Job.Name,
		"session_id": jobResult.Run.ID, "output": jobResult.Output, "error": errorMessage,
		"model": jobResult.Run.Model, "provider": jobResult.Run.BillingProvider,
		"end_reason": jobResult.Run.EndReason,
		"usage": map[string]any{
			"input": jobResult.Run.InputTokens, "output": jobResult.Run.OutputTokens,
			"cache_read": jobResult.Run.CacheReadTokens, "cache_write": jobResult.Run.CacheWriteTokens,
			"reasoning": jobResult.Run.ReasoningTokens,
		},
	}
}

func hermesJobMetadata(jobResult hermes.JobRunResult) map[string]any {
	return map[string]any{
		"external_job_id": jobResult.Job.ID, "external_session_id": jobResult.Run.ID,
		"actual_model": jobResult.Run.Model, "actual_provider": jobResult.Run.BillingProvider,
		"end_reason": jobResult.Run.EndReason,
	}
}

func (a Adapter) runHermes(
	ctx context.Context,
	request Request,
	prompt string,
	stdout io.Writer,
	result Result,
) (Result, error) {
	if a.Contexts == nil {
		return result, fmt.Errorf("hermes harness requires an agent context store")
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
		Env: append(os.Environ(),
			"REDLINE_RUN_ID="+request.RunID,
			"REDLINE_TASK_ID="+request.Task.ID,
			"REDLINE_TASK_NAME="+request.Task.Name,
			"REDLINE_WORKSPACE_DIR="+request.Workspace.Directory,
			"REDLINE_SESSION_ID="+request.Workspace.SessionID,
			"REDLINE_RESULT_FILE="+ResultFilePath(request.OutputDirectory, request.RunID),
		),
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
	if filepath.IsAbs(task.PromptFile) {
		return "", fmt.Errorf("prompt_file must be relative to the workspace, got absolute path %q", task.PromptFile)
	}
	path := filepath.Join(workspaceDirectory, task.PromptFile)
	relative, err := filepath.Rel(workspaceDirectory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("prompt_file %q escapes workspace directory", task.PromptFile)
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
