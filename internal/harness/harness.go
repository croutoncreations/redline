package harness

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/artifacts"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/hermes"
	redprocess "github.com/croutoncreations/redline/internal/process"
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
		strings.HasPrefix(failure, "Codex CLI is signed out.") ||
		strings.HasPrefix(failure, "Cursor CLI is signed out.")
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
	command, cleanup, err := buildCommand(request, prompt, stdout, stderr)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	exitCode, err := a.runner().Run(ctx, command)
	result.ExitCode = exitCode
	if err != nil {
		return result, fmt.Errorf("launch %s harness: %w", request.Profile.HarnessType, err)
	}
	if exitCode != 0 {
		_ = stdout.Sync()
		_ = stderr.Sync()
		result.Failure = diagnoseFailure(request.Profile.HarnessType, result.OutputFile, result.ErrorFile)
		// A profile run's sign-in is that profile's, not the default one.
		// The "Claude Code is signed out." prefix stays, so the run still
		// pauses its account.
		// A Claude API credits run signs in with the account's API key, so
		// the fix is a fresh key, not a login.
		if request.Profile.APICreditsVendor == apiCreditsVendorAnthropic && strings.HasPrefix(result.Failure, "Claude Code is signed out.") {
			result.Failure = "Claude Code is signed out. The Claude API key saved for this account was rejected; run " +
				"`pbpaste | redline account set-api-key " + shellWord(request.Profile.ProviderAccountID) + "` with a key from the linked Console org, then retry this job."
		} else if dir := request.Profile.ClaudeConfigDir; dir != "" && strings.HasPrefix(result.Failure, "Claude Code is signed out.") {
			result.Failure = "Claude Code is signed out. Run `CLAUDE_CONFIG_DIR=" + displayHome(dir) + " claude auth login`, then retry this job."
		}
	}
	return result, nil
}

// shellWord returns value as one shell word for a copy-paste command:
// unchanged when it is plain [A-Za-z0-9._-], single-quoted otherwise.
// Config-file account ids are not limited to that alphabet.
func shellWord(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func diagnoseFailure(harnessType string, paths ...string) string {
	if harnessType == "cursor-agent" && len(paths) == 2 {
		// The Cursor CLI's stdout is the stream-json transcript, which carries
		// the task's own tool output. A task whose `git push` hit a 401 must
		// not read as Cursor being signed out and pause the provider, so only
		// the CLI's stderr is consulted.
		paths = paths[1:]
	}
	var evidence strings.Builder
	for _, path := range paths {
		data, err := artifacts.ReadFileTail(path, 64*1024)
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
	case "cursor-agent":
		// The CLI's own message names its login command; generic phrases
		// such as "authentication required" also come from other tools.
		if strings.Contains(normalized, "run 'agent login'") ||
			strings.Contains(normalized, "run `agent login`") {
			return "Cursor CLI is signed out. Run `agent login`, then retry this job."
		}
	}
	return ""
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

// displayHome shows a path under the home folder as "~/…".
func displayHome(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(dir, filepath.Clean(home)+string(filepath.Separator)); ok {
			return "~/" + rest
		}
	}
	return dir
}

// openAIRoutingVariables would sign a run in as another OpenAI organization
// or project, or send its key to another host.
var openAIRoutingVariables = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE",
	"OPENAI_ORG_ID", "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID", "OPENAI_PROJECT"}

// withOpenAICredentials gives an API credits run exactly its account's key
// and organization, after dropping whatever the service inherited.
func withOpenAICredentials(environment []string, key, organization string) []string {
	for _, name := range openAIRoutingVariables {
		environment = withoutVariable(environment, name)
	}
	environment = append(environment, "OPENAI_API_KEY="+key)
	if organization != "" {
		environment = append(environment, "OPENAI_ORG_ID="+organization, "OPENAI_ORGANIZATION="+organization)
	}
	return environment
}

// API credits vendors, as config.APICreditsVendor names them. The harness
// does not import config; the service fills these in on the run profile.
const (
	apiCreditsVendorOpenAI    = "openai"
	apiCreditsVendorAnthropic = "anthropic"
)

// anthropicRoutingPrefixes and anthropicRoutingVariables would sign a run
// in as another Anthropic account or organization, send its key to another
// host or with another identity's headers, or bill a cloud provider instead
// of the Console organization. Whole families are dropped by prefix, so
// names a new CLI release adds are covered too.
var (
	anthropicRoutingPrefixes  = []string{"ANTHROPIC_", "CLAUDE_CODE_USE_", "CLAUDE_CODE_OAUTH_", "CLAUDE_CODE_API_", "CLAUDE_CODE_GATEWAY_"}
	anthropicRoutingVariables = []string{"CLAUDE_CODE_OAUTH_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"}
)

// withAnthropicCredentials gives a Claude API credits run exactly its
// account's key, after dropping whatever the service inherited.
func withAnthropicCredentials(environment []string, key string) []string {
	return anthropicCredentialEnvironment(runtime.GOOS, environment, key)
}

func anthropicCredentialEnvironment(goos string, environment []string, key string) []string {
	environment = redprocess.WithoutPrefixes(goos, environment, anthropicRoutingPrefixes...)
	environment = redprocess.WithoutVariables(goos, environment, anthropicRoutingVariables...)
	return append(environment, "ANTHROPIC_API_KEY="+key)
}

//go:embed pi_anthropic_credits.ts
var piAnthropicCreditsExtension string

// PiAnthropicCreditsExtension is the pi extension a Claude API credits pi
// run loads: it moves anthropic models onto a provider that reads
// $ANTHROPIC_API_KEY, ahead of any stored Anthropic OAuth login.
func PiAnthropicCreditsExtension() string { return piAnthropicCreditsExtension }

// Run scratch directory name prefixes, under os.TempDir().
const (
	claudeCreditsScratchPrefix = "redline-claude-credits-"
	piCreditsScratchPrefix     = "redline-pi-credits-"
)

// staleRunScratchAge is how old a scratch directory must be before the
// startup sweep treats it as left behind by a crashed service rather than
// held by a run still going.
const staleRunScratchAge = 24 * time.Hour

// SweepRunScratch removes run scratch directories a crashed or killed
// service left in os.TempDir(): the Claude Code one may hold a run's session
// transcript. Call it once at service start; failures are logged without
// the directory's contents.
func SweepRunScratch(now time.Time) {
	removed, errs := sweepRunScratch(os.TempDir(), now)
	if removed > 0 {
		log.Printf("redline: removed %d stale run scratch directories", removed)
	}
	for _, err := range errs {
		log.Printf("redline: could not remove stale run scratch directory: %v", err)
	}
}

// sweepRunScratch removes Redline run scratch directories in temp that are
// owned by this user and older than staleRunScratchAge.
func sweepRunScratch(temp string, now time.Time) (int, []error) {
	entries, err := os.ReadDir(temp)
	if err != nil {
		return 0, []error{err}
	}
	removed := 0
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !(strings.HasPrefix(name, claudeCreditsScratchPrefix) || strings.HasPrefix(name, piCreditsScratchPrefix)) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !ownedByCurrentUser(info) || now.Sub(info.ModTime()) < staleRunScratchAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(temp, name)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errs
}

// runScratch holds a run's private, run-scoped files (an empty Claude Code
// config directory, the pi extension) and removes them when the run ends.
type runScratch struct{ dirs []string }

func (r *runScratch) dir(pattern string) (string, error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	r.dirs = append(r.dirs, dir)
	return dir, nil
}

func (r *runScratch) cleanup() {
	for _, dir := range r.dirs {
		if err := os.RemoveAll(dir); err != nil {
			// The path is a random temp name; the error carries no credential.
			// The startup sweep retries it.
			log.Printf("redline: could not remove run scratch directory: %v", err)
		}
	}
}

// claudeCreditsConfigDir gives a Claude credits run that may start Claude
// Code a fresh, empty, private CLAUDE_CONFIG_DIR, after dropping any the
// service inherited, so no stored login can take precedence over the key.
func claudeCreditsConfigDir(environment []string, scratch *runScratch) ([]string, error) {
	dir, err := scratch.dir(claudeCreditsScratchPrefix)
	if err != nil {
		return nil, fmt.Errorf("create Claude Code config directory: %w", err)
	}
	return append(withoutVariable(environment, "CLAUDE_CONFIG_DIR"), "CLAUDE_CONFIG_DIR="+dir), nil
}

// writePiExtension writes the pin extension into a private run directory.
func (r *runScratch) writePiExtension() (string, error) {
	dir, err := r.dir(piCreditsScratchPrefix)
	if err != nil {
		return "", fmt.Errorf("create pi extension directory: %w", err)
	}
	path := filepath.Join(dir, "redline-anthropic-credits.ts")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create pi extension: %w", err)
	}
	_, writeErr := file.WriteString(piAnthropicCreditsExtension)
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		return "", fmt.Errorf("write pi extension")
	}
	return path, nil
}

// withoutVariable drops name from environment, ignoring case on Windows.
func withoutVariable(environment []string, name string) []string {
	return redprocess.WithoutVariables(runtime.GOOS, environment, name)
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

// buildCommand returns the harness command and a cleanup that removes the
// run's scratch files; cleanup is safe to call whatever the outcome.
func buildCommand(
	request Request,
	prompt string,
	stdout, stderr io.Writer,
) (redprocess.Command, func(), error) {
	scratch := &runScratch{}
	command, err := buildHarnessCommand(request, prompt, stdout, stderr, scratch)
	if err != nil {
		scratch.cleanup()
		return redprocess.Command{}, func() {}, err
	}
	return command, scratch.cleanup, nil
}

func buildHarnessCommand(
	request Request,
	prompt string,
	stdout, stderr io.Writer,
	scratch *runScratch,
) (redprocess.Command, error) {
	base := redprocess.Command{
		Dir: request.Workspace.Directory, Stdout: stdout, Stderr: stderr,
		Env: append(redprocess.ServiceEnvironment(),
			"REDLINE_RUN_ID="+request.RunID,
			"REDLINE_TASK_ID="+request.Task.ID,
			"REDLINE_TASK_NAME="+request.Task.Name,
			"REDLINE_WORKSPACE_DIR="+request.Workspace.Directory,
			"REDLINE_SESSION_ID="+request.Workspace.SessionID,
			"REDLINE_RESULT_FILE="+ResultFilePath(request.OutputDirectory, request.RunID),
		),
	}
	// The platform session key carries full dashboard access; no run may
	// see it, even if the service was started with it in its environment.
	// The run markers below are Redline's to set, never inherited.
	base.Env = redprocess.WithoutServiceOnlyVariables(base.Env)
	if account := request.Profile.ProviderAccountID; account != "" {
		base.Env = append(base.Env, "REDLINE_PROVIDER_ACCOUNT_ID="+account)
	}
	anthropicCredits := false
	if key := request.Profile.APICreditsKey; key != "" {
		switch request.Profile.APICreditsVendor {
		case apiCreditsVendorOpenAI:
			base.Env = withOpenAICredentials(base.Env, key, request.Profile.APICreditsOrganization)
			// Tools inside the run (such as a pi extension that moves OpenAI
			// models to a ChatGPT subscription) use this to keep billing the key.
			base.Env = append(base.Env, "REDLINE_OPENAI_API_CREDITS=1")
		case apiCreditsVendorAnthropic:
			base.Env = withAnthropicCredentials(base.Env, key)
			// The pi extension below, and pi-config's provider priority, use
			// this to keep anthropic models on the key.
			base.Env = append(base.Env, "REDLINE_ANTHROPIC_API_CREDITS=1")
			anthropicCredits = true
		default:
			return redprocess.Command{}, fmt.Errorf("unknown API credits vendor %q", request.Profile.APICreditsVendor)
		}
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
		// The account's Claude Code profile, for this run only. Any
		// CLAUDE_CONFIG_DIR inherited from the service is dropped first, so
		// a default-login account never runs on a profile by accident.
		base.Env = withoutVariable(base.Env, "CLAUDE_CONFIG_DIR")
		if anthropicCredits {
			// Claude Code prefers a stored login over ANTHROPIC_API_KEY, so
			// a credits run starts from a fresh, empty, private config
			// directory of its own and never sees any profile's login.
			env, err := claudeCreditsConfigDir(base.Env, scratch)
			if err != nil {
				return redprocess.Command{}, err
			}
			base.Env = env
		} else if dir := request.Profile.ClaudeConfigDir; dir != "" {
			// Variables that would sign Claude Code in as someone else
			// take precedence over the profile's login; drop them so the
			// run spends only this profile's allowance.
			// The routing variables would send the profile's token to
			// another host or bill another cloud account.
			for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
				"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
				base.Env = withoutVariable(base.Env, name)
			}
			base.Env = append(base.Env, "CLAUDE_CONFIG_DIR="+dir)
		}
		base.Args = []string{"--print", "--output-format", "stream-json", "--verbose", "--permission-mode", "auto"}
		if request.Profile.Model != "" && request.Profile.Model != "default" {
			base.Args = append(base.Args, "--model", request.Profile.Model)
		}
		if anthropicCredits {
			// The empty config directory does not cover project settings: a
			// workspace .claude/settings.json could set ANTHROPIC_BASE_URL and
			// send the key elsewhere. Load no settings sources at all.
			base.Args = append(base.Args, "--setting-sources=")
		}
		base.Args = append(base.Args, request.Profile.HarnessArgs...)
		base.Args = append(base.Args, "--append-system-prompt", claudeWorkspaceBoundary(request.Workspace.Directory))
		base.Stdin = strings.NewReader(prompt)
	case "cursor-agent":
		// Cursor's headless CLI, run sandboxed. Measured against CLI
		// 2026.10.01: with --sandbox enabled and without --force, the agent
		// edits files and runs commands inside the workspace, while writes
		// elsewhere and network access are refused. --force overrides the
		// sandbox and lets every command run, so it is never passed here; a
		// profile that needs it must say so in harness_args. --trust skips
		// the workspace-trust prompt, which would otherwise block a
		// non-interactive run in Redline's own workspace.
		//
		// The binary is cursor-agent rather than agent: the installer
		// provides both, and "agent" is a common name for unrelated tools.
		// The model is always passed so the run draws on the pool it was
		// scheduled against: "default" would otherwise follow the CLI's own
		// setting, which can be a model in the other pool.
		base.Name = "cursor-agent"
		base.Env = redprocess.WithoutCursorCredentials(base.Env)
		model := request.Profile.Model
		if model == "" || model == "default" {
			model = "auto"
		}
		base.Args = []string{"--print", "--output-format", "stream-json", "--trust",
			"--sandbox", "enabled", "--workspace", request.Workspace.Directory, "--model", model}
		base.Args = append(base.Args, request.Profile.HarnessArgs...)
		base.Stdin = strings.NewReader(prompt)
	case "pi":
		base.Name = "pi"
		base.Args = []string{"--print", "--mode", "json", "--name", "redline-" + request.RunID}
		if request.Profile.Model != "" && request.Profile.Model != "default" {
			base.Args = append(base.Args, "--model", request.Profile.Model)
		}
		if anthropicCredits {
			path, err := scratch.writePiExtension()
			if err != nil {
				return redprocess.Command{}, err
			}
			base.Args = append(base.Args, "-e", path)
		}
		base.Args = append(base.Args, request.Profile.HarnessArgs...)
		base.Stdin = strings.NewReader(prompt)
	case "command":
		if request.Profile.HarnessCommand == "" {
			return redprocess.Command{}, fmt.Errorf("command harness requires harness_command")
		}
		if anthropicCredits {
			// The command may start claude itself; give it the same empty
			// config directory as a claude-code run. A command that starts
			// pi must load the pin extension itself (see docs/scheduling.md).
			env, err := claudeCreditsConfigDir(base.Env, scratch)
			if err != nil {
				return redprocess.Command{}, err
			}
			base.Env = env
		}
		base.Name, base.Args = redprocess.ShellCommand(request.Profile.HarnessCommand)
		base.Stdin = strings.NewReader(prompt)
	default:
		return redprocess.Command{}, fmt.Errorf("unsupported harness %q", request.Profile.HarnessType)
	}
	return base, nil
}

// cursorReservedArgs are flags Redline owns for a Cursor run. A profile that
// set them could move the run out of its workspace, or authenticate it as
// another account with a key that would then be stored with the profile.
var cursorReservedArgs = []string{"--api-key", "--endpoint", "-e", "--workspace", "--add-dir"}

// ValidateHarnessArgs rejects harness arguments that would override what
// Redline relies on for a harness.
func ValidateHarnessArgs(harnessType string, args []string) error {
	if harnessType != "cursor-agent" {
		return nil
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(cursorReservedArgs, name) {
			return fmt.Errorf("harness_args may not set %s for cursor-agent: Redline sets the workspace, and the CLI uses its own `agent login`", name)
		}
	}
	return nil
}

// creditsReservedArgs are flags a profile on an API credits account may not
// pass, by vendor and harness: --api-key would put a key in argv, and the
// rest would move a Claude credits run off the account's key (another
// provider, another extension ahead of or instead of the pin, or Claude Code
// settings carrying their own credentials or endpoint). pi's --model and
// --models are refused because the profile's model is the one checked
// against the account's vendor. pi's --no-extensions is allowed: it leaves
// the explicit -e pin loaded.
var creditsReservedArgs = map[string]map[string][]string{
	"anthropic": {
		"pi":          {"--api-key", "--provider", "-e", "--extension", "--model", "--models"},
		"claude-code": {"--settings", "--setting-sources"},
	},
	"openai": {
		"pi": {"--api-key", "--model", "--models"},
	},
}

// ValidateCreditsHarnessArgs rejects harness arguments that would move a
// run on an API credits account (vendor "openai" or "anthropic") off the
// account's key, or put the key in argv. vendor "" is not a credits account.
func ValidateCreditsHarnessArgs(harnessType, vendor string, args []string) error {
	reserved := creditsReservedArgs[vendor][harnessType]
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if slices.Contains(reserved, name) {
			return fmt.Errorf("harness_args %s is unsupported for %s on an API credits account: Redline gives the run the account's key and keeps it on that key", name, harnessType)
		}
	}
	return nil
}

func claudeWorkspaceBoundary(directory string) string {
	return fmt.Sprintf(
		`Your exact workspace directory is %q. Perform all task work inside that directory. `+
			`Resolve every relative path from that directory, and use that directory - not another checkout - as the project root. `+
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
	if err := confineToWorkspace(workspaceDirectory, path, task.PromptFile); err != nil {
		return "", err
	}
	// The lexical check cannot see through symlinks, so re-check against the
	// resolved paths before reading. A symlink inside the workspace pointing
	// outside it would otherwise read an arbitrary file — credentials, keys —
	// straight into the model prompt.
	//
	// Both sides are resolved: the workspace root itself is commonly reached
	// through a symlink (on macOS /tmp and /var are symlinked), so comparing a
	// resolved file against an unresolved root would reject legitimate reads.
	resolvedRoot, err := filepath.EvalSymlinks(workspaceDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Keep a missing prompt_file reported as a read failure rather than a
		// resolution failure, matching the behaviour callers already expect.
		if os.IsNotExist(err) {
			return "", fmt.Errorf("read task prompt: %w", err)
		}
		return "", fmt.Errorf("resolve prompt_file %q: %w", task.PromptFile, err)
	}
	if err := confineToWorkspace(resolvedRoot, resolvedPath, task.PromptFile); err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read task prompt: %w", err)
	}
	return string(data), nil
}

// confineToWorkspace reports whether path stays within root, treating any
// upward traversal as an escape.
func confineToWorkspace(root, path, promptFile string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("prompt_file %q escapes workspace directory", promptFile)
	}
	return nil
}

func (a Adapter) runner() redprocess.Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return redprocess.ExecRunner{}
}
