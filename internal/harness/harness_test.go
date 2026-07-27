package harness_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/harness"
	"github.com/jfox/redline/internal/hermes"
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
	wantPrefix := "--print --output-format stream-json --verbose --permission-mode auto --model sonnet --effort high --append-system-prompt"
	if runner.command.Name != "claude" || !strings.HasPrefix(strings.Join(runner.command.Args, " "), wantPrefix) ||
		runner.command.Dir != "/tmp/work" || runner.stdin != "review auth" {
		t.Fatalf("command=%#v stdin=%q", runner.command, runner.stdin)
	}
	systemPrompt := runner.command.Args[len(runner.command.Args)-1]
	if !strings.Contains(systemPrompt, `"/tmp/work"`) ||
		!strings.Contains(systemPrompt, "Do not read or modify a parent checkout") {
		t.Fatalf("workspace system prompt = %q", systemPrompt)
	}
}

func TestPiAdapterBuildsNoninteractiveNamedSession(t *testing.T) {
	runner := &captureRunner{}
	_, err := (harness.Adapter{Runner: runner}).Run(context.Background(), harness.Request{
		RunID: "run-pi", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "reply with ok"},
		Profile:   domain.ExecutionProfile{HarnessType: "pi", Model: "openai-codex/gpt-5.6-sol", HarnessArgs: []string{"--thinking", "low"}},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "--print --mode json --name redline-run-pi --model openai-codex/gpt-5.6-sol --thinking low"
	if runner.command.Name != "pi" || strings.Join(runner.command.Args, " ") != want || runner.stdin != "reply with ok" {
		t.Fatalf("command=%#v stdin=%q", runner.command, runner.stdin)
	}
}

func TestAdaptersPassMinimalCustomizationFlags(t *testing.T) {
	tests := []struct {
		name        string
		harnessType string
		args        []string
	}{
		{
			name: "codex", harnessType: "codex-cli",
			args: []string{"--ignore-user-config", "--ignore-rules", "--disable", "plugins", "--disable", "hooks", "--disable", "apps", "--sandbox", "read-only", "--ephemeral"},
		},
		{
			name: "claude", harnessType: "claude-code",
			args: []string{"--safe-mode", "--tools", "", "--no-session-persistence"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &captureRunner{}
			_, err := (harness.Adapter{Runner: runner}).Run(context.Background(), harness.Request{
				RunID: "minimal", OutputDirectory: t.TempDir(),
				Task:      domain.Task{ID: "task", Prompt: "reply ok"},
				Profile:   domain.ExecutionProfile{HarnessType: test.harnessType, HarnessArgs: test.args},
				Workspace: domain.Workspace{Directory: "/tmp/work"},
			})
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string(nil), test.args...)
			if test.harnessType == "codex-cli" {
				want = append(want, "-")
			}
			got := runner.command.Args
			if test.harnessType == "codex-cli" {
				got = got[len(got)-len(want):]
			} else {
				got = got[len(got)-len(want)-2 : len(got)-2]
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("args = %#v", runner.command.Args)
			}
		})
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

func TestHermesHarnessUsesAgentContextAndPersistsExternalSession(t *testing.T) {
	contexts := fakeContexts{
		context: domain.AgentContext{
			ID: "hermes-default", RuntimeConnectionID: "hermes-pi",
			Profile: "default", WorkingDirectory: "/srv/redline",
		},
		connection: domain.RuntimeConnection{
			ID: "hermes-pi", Runtime: "hermes", Transport: "gateway", URL: "http://gateway.test",
		},
	}
	runner := &fakeHermesRunner{result: hermes.RunResult{
		SessionID: "session-1", Output: "OK", Model: "gpt-5.5", Provider: "openai-codex",
		Usage: map[string]any{"input": 8, "output": 1},
	}}
	var external domain.ExternalRun
	result, err := (harness.Adapter{Contexts: contexts, Hermes: runner}).Run(t.Context(), harness.Request{
		RunID: "run-hermes", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "Reply OK."},
		Profile: domain.ExecutionProfile{
			AgentContextID: contexts.context.ID, HarnessType: "hermes",
			Model: "openai-codex/gpt-5.5",
		},
		Workspace: domain.Workspace{Directory: "/srv/redline"},
		OnExternalStarted: func(value domain.ExternalRun) error {
			external = value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.Connection.ID != "hermes-pi" || runner.request.Context.Profile != "default" ||
		runner.request.Provider != "openai-codex" || runner.request.Model != "gpt-5.5" {
		t.Fatalf("request = %#v", runner.request)
	}
	if result.ExitCode != 0 || result.Metadata["external_session_id"] != "session-1" {
		t.Fatalf("result = %#v", result)
	}
	if external.SessionID != "session-1" {
		t.Fatalf("external = %#v", external)
	}
	data, err := os.ReadFile(result.OutputFile)
	if err != nil || !strings.Contains(string(data), `"output":"OK"`) {
		t.Fatalf("artifact=%s err=%v", data, err)
	}
}

func TestHermesHarnessCanTriggerExistingRuntimeJobWithoutPrompt(t *testing.T) {
	contexts := fakeContexts{
		context: domain.AgentContext{
			ID: "hermes-default", RuntimeConnectionID: "hermes-pi",
			Profile: "default", WorkingDirectory: "/srv/redline",
		},
		connection: domain.RuntimeConnection{
			ID: "hermes-pi", Runtime: "hermes", Transport: "gateway", URL: "http://gateway.test",
		},
	}
	runner := &fakeHermesRunner{jobResult: hermes.JobRunResult{
		Job: hermes.Job{
			ID: "job-seo-planner", Name: "Weekly SEO content planner",
			Provider: "custom:cliproxyapi-plus", Model: "claude-fable-5-medium", Enabled: true,
		},
		Run: hermes.JobRun{
			ID: "cron_job-seo-planner_20260728_100214", EndReason: "cron_complete",
			Model: "claude-fable-5-medium", BillingProvider: "custom:cliproxyapi-plus",
			InputTokens: 120, OutputTokens: 8, CacheReadTokens: 400,
		},
		Output: "Finished the content plan.",
	}}
	var external domain.ExternalRun
	result, err := (harness.Adapter{Contexts: contexts, Hermes: runner}).Run(t.Context(), harness.Request{
		RunID: "run-hermes-job", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", RuntimeJobID: runner.jobResult.Job.ID},
		Profile: domain.ExecutionProfile{
			AgentContextID: contexts.context.ID, HarnessType: "hermes",
			Model: "custom:cliproxyapi-plus/claude-fable-5-medium",
		},
		Workspace: domain.Workspace{Directory: "/srv/redline"},
		OnExternalStarted: func(value domain.ExternalRun) error {
			external = value
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.jobRequest.Connection.ID != "hermes-pi" || runner.jobRequest.JobID != runner.jobResult.Job.ID {
		t.Fatalf("connection=%#v job=%q", runner.triggeredConnection, runner.triggeredJobID)
	}
	if external.RuntimeConnectionID != "hermes-pi" || external.RunID != runner.jobResult.Job.ID ||
		external.SessionID != runner.jobResult.Run.ID {
		t.Fatalf("external = %#v", external)
	}
	if result.ExitCode != 0 || result.Metadata["external_job_id"] != runner.jobResult.Job.ID ||
		result.Metadata["external_session_id"] != runner.jobResult.Run.ID {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(result.OutputFile)
	if err != nil || !strings.Contains(string(data), `"job_id":"job-seo-planner"`) ||
		!strings.Contains(string(data), `"output":"Finished the content plan."`) {
		t.Fatalf("artifact=%s err=%v", data, err)
	}
}

func TestHermesHarnessPersistsFailedRuntimeJobUsage(t *testing.T) {
	contexts := fakeContexts{
		context:    domain.AgentContext{ID: "hermes-default", RuntimeConnectionID: "hermes-pi"},
		connection: domain.RuntimeConnection{ID: "hermes-pi", Runtime: "hermes", Transport: "gateway"},
	}
	runner := &fakeHermesRunner{
		jobResult: hermes.JobRunResult{
			Job: hermes.Job{ID: "broken"},
			Run: hermes.JobRun{
				ID: "cron_broken_new", EndReason: "cron_complete",
				Model: "claude-fable-5-medium", BillingProvider: "custom:cliproxyapi-plus",
				InputTokens: 90, OutputTokens: 3,
			},
		},
		jobErr: fmt.Errorf("provider rejected request"),
	}
	result, err := (harness.Adapter{Contexts: contexts, Hermes: runner}).Run(t.Context(), harness.Request{
		RunID: "run-hermes-failed", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", RuntimeJobID: "broken"},
		Profile:   domain.ExecutionProfile{AgentContextID: contexts.context.ID, HarnessType: "hermes"},
		Workspace: domain.Workspace{Directory: "/srv/redline"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider rejected request") || result.ExitCode != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	data, readErr := os.ReadFile(result.OutputFile)
	if readErr != nil || !strings.Contains(string(data), `"input":90`) ||
		!strings.Contains(string(data), `"error":"provider rejected request"`) {
		t.Fatalf("artifact=%s err=%v", data, readErr)
	}
}

type captureRunner struct {
	command redprocess.Command
	stdin   string
}

type fakeContexts struct {
	context    domain.AgentContext
	connection domain.RuntimeConnection
}

func (f fakeContexts) GetAgentContext(context.Context, string) (domain.AgentContext, error) {
	return f.context, nil
}

func (f fakeContexts) GetRuntimeConnection(context.Context, string) (domain.RuntimeConnection, error) {
	return f.connection, nil
}

type fakeHermesRunner struct {
	request             hermes.RunRequest
	result              hermes.RunResult
	jobResult           hermes.JobRunResult
	jobErr              error
	jobRequest          hermes.JobRunRequest
	triggeredConnection domain.RuntimeConnection
	triggeredJobID      string
}

func (f *fakeHermesRunner) Run(_ context.Context, request hermes.RunRequest) (hermes.RunResult, error) {
	f.request = request
	if request.OnExternalStarted != nil {
		_ = request.OnExternalStarted(domain.ExternalRun{
			RuntimeConnectionID: request.Connection.ID, RunID: request.RunID, SessionID: f.result.SessionID,
		})
	}
	return f.result, nil
}

func (f *fakeHermesRunner) TriggerJob(_ context.Context, connection domain.RuntimeConnection, jobID string) (hermes.Job, error) {
	f.triggeredConnection = connection
	f.triggeredJobID = jobID
	return f.jobResult.Job, nil
}

func (f *fakeHermesRunner) RunJob(_ context.Context, request hermes.JobRunRequest) (hermes.JobRunResult, error) {
	f.jobRequest = request
	if request.OnExternalStarted != nil {
		_ = request.OnExternalStarted(domain.ExternalRun{
			RuntimeConnectionID: request.Connection.ID,
			RunID:               f.jobResult.Job.ID,
			SessionID:           f.jobResult.Run.ID,
		})
	}
	return f.jobResult, f.jobErr
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
