// Package mcpserver exposes Redline's service API to local MCP clients.
// It intentionally does not open the SQLite database: redline serve remains
// the single owner of persistence and operational validation.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jfox/redline/internal/apiclient"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/hermes"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100
	defaultLogTail   = 8 * 1024
	maxLogTail       = 32 * 1024
	maxPromptBytes   = 8 * 1024
	maxEventPayload  = 8 * 1024
	maxOverviewItems = 20
)

type server struct {
	client apiclient.Client
}

// Output is the stable envelope returned by every Redline MCP tool.
type Output struct {
	Summary   string `json:"summary" jsonschema:"short agent-oriented explanation of the result"`
	Count     int    `json:"count,omitempty" jsonschema:"number of returned items when the result is a list"`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"whether additional items were omitted"`
	Data      any    `json:"data,omitempty" jsonschema:"structured Redline API result"`
}

type noInput struct{}

type providerInput struct {
	ProviderAccountID string `json:"provider_account_id" jsonschema:"configured Redline provider account ID, for example codex-main or claude-main"`
}

type listInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum number of items to return; defaults to 20 and is capped at 100"`
}

type idInput struct {
	ID string `json:"id" jsonschema:"resource ID"`
}

type runInput struct {
	RunID string `json:"run_id" jsonschema:"Redline run ID"`
}

type runEventsInput struct {
	RunID string `json:"run_id" jsonschema:"Redline run ID"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum events to return; defaults to 20 and is capped at 100"`
}

type runLogsInput struct {
	RunID    string `json:"run_id" jsonschema:"Redline run ID"`
	Stream   string `json:"stream,omitempty" jsonschema:"stdout, stderr, prepare_stdout, prepare_stderr, finalize_stdout, or finalize_stderr"`
	TailByte int64  `json:"tail_bytes,omitempty" jsonschema:"bytes to read from the end; defaults to 8192 and is capped at 32768"`
}

type taskCreateInput struct {
	ID                 string `json:"id,omitempty" jsonschema:"optional stable task ID"`
	Name               string `json:"name" jsonschema:"human-readable task name"`
	Prompt             string `json:"prompt,omitempty" jsonschema:"agent instructions; use prompt_file instead when appropriate"`
	PromptFile         string `json:"prompt_file,omitempty" jsonschema:"path to a prompt file available to the Redline service"`
	Priority           int    `json:"priority" jsonschema:"higher values run before lower values within the unlocked dispatch tier"`
	ExecutionProfileID string `json:"execution_profile_id" jsonschema:"existing execution profile ID"`
	RuntimeJobID       string `json:"runtime_job_id,omitempty" jsonschema:"existing Hermes job ID to trigger instead of starting a new prompt session"`
	Type               string `json:"type" jsonschema:"one_off or recurring"`
	DispatchTier       string `json:"dispatch_tier" jsonschema:"behind, well_behind, or expiring"`
	MinInterval        string `json:"min_interval,omitempty" jsonschema:"minimum interval between recurring runs, such as 6h or 7d"`
	RequireRepoChange  bool   `json:"require_repo_change,omitempty" jsonschema:"only rerun after the source revision changes"`
}

type taskUpdateInput struct {
	ID                 string  `json:"id" jsonschema:"task ID"`
	Name               *string `json:"name,omitempty" jsonschema:"new human-readable name"`
	Prompt             *string `json:"prompt,omitempty" jsonschema:"new inline agent instructions"`
	PromptFile         *string `json:"prompt_file,omitempty" jsonschema:"new prompt file path"`
	Priority           *int    `json:"priority,omitempty" jsonschema:"new priority"`
	ExecutionProfileID *string `json:"execution_profile_id,omitempty" jsonschema:"new execution profile ID"`
	RuntimeJobID       *string `json:"runtime_job_id,omitempty" jsonschema:"new existing Hermes job ID; empty clears it"`
	Type               *string `json:"type,omitempty" jsonschema:"one_off or recurring"`
	DispatchTier       *string `json:"dispatch_tier,omitempty" jsonschema:"behind, well_behind, or expiring"`
	MinInterval        *string `json:"min_interval,omitempty" jsonschema:"new minimum interval; an empty string clears it"`
	RequireRepoChange  *bool   `json:"require_repo_change,omitempty" jsonschema:"whether source revision must change before rerun"`
}

type taskControlInput struct {
	ID      string `json:"id" jsonschema:"task ID"`
	Control string `json:"control" jsonschema:"enable, disable, or retry"`
}

type providerControlInput struct {
	ProviderAccountID string `json:"provider_account_id" jsonschema:"configured Redline provider account ID"`
	Control           string `json:"control" jsonschema:"pause or resume"`
}

type providerConcurrencyInput struct {
	ProviderAccountID string `json:"provider_account_id" jsonschema:"configured Redline provider account ID"`
	MaxConcurrentRuns int    `json:"max_concurrent_runs" jsonschema:"provider parallel run limit; use 0 to restore the configured default"`
}

type schedulerInput struct {
	ProviderAccountID string `json:"provider_account_id" jsonschema:"configured Redline provider account ID"`
	CurrentRevision   string `json:"current_revision,omitempty" jsonschema:"current repository commit SHA for repo-change eligibility"`
}

type profileCreateInput struct {
	ID                string   `json:"id,omitempty" jsonschema:"optional stable profile ID"`
	ProviderAccountID string   `json:"provider_account_id" jsonschema:"configured Redline provider account ID"`
	AgentContextID    string   `json:"agent_context_id,omitempty" jsonschema:"Hermes agent context ID when harness_type is hermes"`
	HarnessType       string   `json:"harness_type" jsonschema:"codex-cli, claude-code, pi, command, or another supported harness"`
	Model             string   `json:"model,omitempty" jsonschema:"model identifier passed to the harness"`
	BudgetModelGroup  string   `json:"budget_model_group,omitempty" jsonschema:"optional model-specific allowance routing override such as fable"`
	HarnessCommand    string   `json:"harness_command,omitempty" jsonschema:"custom harness command when harness_type is command"`
	HarnessArgs       []string `json:"harness_args,omitempty" jsonschema:"additional harness command arguments"`
	WorkspaceProvider string   `json:"workspace_provider" jsonschema:"devx, git-worktree, existing-directory, or command"`
	WorkspaceArgs     []string `json:"workspace_args,omitempty" jsonschema:"additional workspace-provider arguments"`
	Repository        string   `json:"repository,omitempty" jsonschema:"absolute repository or working-directory path"`
	BaseBranch        string   `json:"base_branch,omitempty" jsonschema:"base branch used for isolated workspaces"`
	RequireClean      bool     `json:"require_clean,omitempty" jsonschema:"require a clean existing checkout"`
	CleanupPolicy     string   `json:"cleanup_policy,omitempty" jsonschema:"never, on_success, always, or empty for the default"`
	PrepareCommand    string   `json:"prepare_command,omitempty" jsonschema:"optional shell command run before the harness"`
	FinalizeCommand   string   `json:"finalize_command,omitempty" jsonschema:"optional shell command run after the harness"`
}

type profileUpdateInput struct {
	ID                string    `json:"id" jsonschema:"execution profile ID"`
	ProviderAccountID *string   `json:"provider_account_id,omitempty" jsonschema:"new configured provider account ID"`
	AgentContextID    *string   `json:"agent_context_id,omitempty" jsonschema:"new Hermes agent context ID"`
	HarnessType       *string   `json:"harness_type,omitempty" jsonschema:"new harness type"`
	Model             *string   `json:"model,omitempty" jsonschema:"new model identifier; an empty string clears it"`
	BudgetModelGroup  *string   `json:"budget_model_group,omitempty" jsonschema:"new allowance routing override; an empty string clears it"`
	HarnessCommand    *string   `json:"harness_command,omitempty" jsonschema:"new custom harness command; an empty string clears it"`
	HarnessArgs       *[]string `json:"harness_args,omitempty" jsonschema:"replacement harness argument list"`
	WorkspaceProvider *string   `json:"workspace_provider,omitempty" jsonschema:"new workspace provider"`
	WorkspaceArgs     *[]string `json:"workspace_args,omitempty" jsonschema:"replacement workspace-provider argument list"`
	Repository        *string   `json:"repository,omitempty" jsonschema:"new repository path; an empty string clears it"`
	BaseBranch        *string   `json:"base_branch,omitempty" jsonschema:"new base branch; an empty string clears it"`
	RequireClean      *bool     `json:"require_clean,omitempty" jsonschema:"whether a clean existing checkout is required"`
	CleanupPolicy     *string   `json:"cleanup_policy,omitempty" jsonschema:"new cleanup policy; an empty string clears it"`
	PrepareCommand    *string   `json:"prepare_command,omitempty" jsonschema:"new prepare command; an empty string clears it"`
	FinalizeCommand   *string   `json:"finalize_command,omitempty" jsonschema:"new finalize command; an empty string clears it"`
}

type runtimeConnectionCreateInput struct {
	ID                string `json:"id" jsonschema:"stable runtime connection ID"`
	Runtime           string `json:"runtime" jsonschema:"runtime type; currently hermes"`
	Transport         string `json:"transport" jsonschema:"gateway or local"`
	URL               string `json:"url,omitempty" jsonschema:"Hermes Gateway base URL"`
	CredentialSource  string `json:"credential_source,omitempty" jsonschema:"hermes_desktop, environment, file, or empty for an ungated endpoint"`
	CredentialRef     string `json:"credential_ref,omitempty" jsonschema:"environment variable name or credential JSON file path; never the secret itself"`
	DesktopConfigPath string `json:"desktop_config_path,omitempty" jsonschema:"optional Hermes Desktop connection.json path"`
	MaxConcurrentRuns int    `json:"max_concurrent_runs,omitempty" jsonschema:"maximum simultaneous runs through this connection; defaults to 1"`
}

type runtimeConnectionUpdateInput struct {
	ID                string  `json:"id" jsonschema:"runtime connection ID"`
	Runtime           *string `json:"runtime,omitempty" jsonschema:"new runtime type"`
	Transport         *string `json:"transport,omitempty" jsonschema:"new transport"`
	URL               *string `json:"url,omitempty" jsonschema:"new Gateway base URL"`
	CredentialSource  *string `json:"credential_source,omitempty" jsonschema:"new credential source"`
	CredentialRef     *string `json:"credential_ref,omitempty" jsonschema:"new environment variable name or credential file path"`
	DesktopConfigPath *string `json:"desktop_config_path,omitempty" jsonschema:"new Hermes Desktop config path"`
	MaxConcurrentRuns *int    `json:"max_concurrent_runs,omitempty" jsonschema:"new concurrency limit"`
}

type runtimeConnectionDiscoverInput struct {
	ID            string `json:"id" jsonschema:"runtime connection ID"`
	Profile       string `json:"profile,omitempty" jsonschema:"optional runtime profile name"`
	Provider      string `json:"provider,omitempty" jsonschema:"optional provider slug such as anthropic or openai-codex"`
	IncludeModels bool   `json:"include_models,omitempty" jsonschema:"include a bounded page of model identifiers; defaults to false"`
	ModelOffset   int    `json:"model_offset,omitempty" jsonschema:"zero-based model offset within each matching provider"`
	ModelLimit    int    `json:"model_limit,omitempty" jsonschema:"models per matching provider; defaults to 50 and is capped at 200"`
}

type agentContextCreateInput struct {
	ID                  string `json:"id" jsonschema:"stable agent context ID"`
	RuntimeConnectionID string `json:"runtime_connection_id" jsonschema:"existing runtime connection ID"`
	Profile             string `json:"profile,omitempty" jsonschema:"Hermes profile name"`
	Agent               string `json:"agent,omitempty" jsonschema:"runtime-specific agent identifier when supported"`
	Project             string `json:"project,omitempty" jsonschema:"Hermes project identifier"`
	WorkingDirectory    string `json:"working_directory,omitempty" jsonschema:"working directory on the Hermes host"`
	SessionMode         string `json:"session_mode,omitempty" jsonschema:"isolated or persistent; isolated is recommended"`
	MaxConcurrentRuns   int    `json:"max_concurrent_runs,omitempty" jsonschema:"maximum simultaneous runs in this context; defaults to 1"`
}

type agentContextUpdateInput struct {
	ID                  string  `json:"id" jsonschema:"agent context ID"`
	RuntimeConnectionID *string `json:"runtime_connection_id,omitempty" jsonschema:"new runtime connection ID"`
	Profile             *string `json:"profile,omitempty" jsonschema:"new Hermes profile name"`
	Agent               *string `json:"agent,omitempty" jsonschema:"new runtime-specific agent identifier"`
	Project             *string `json:"project,omitempty" jsonschema:"new Hermes project identifier"`
	WorkingDirectory    *string `json:"working_directory,omitempty" jsonschema:"new remote working directory"`
	SessionMode         *string `json:"session_mode,omitempty" jsonschema:"isolated or persistent"`
	MaxConcurrentRuns   *int    `json:"max_concurrent_runs,omitempty" jsonschema:"new context concurrency limit"`
}

type taskView struct {
	ID                           string              `json:"id"`
	Name                         string              `json:"name"`
	Prompt                       string              `json:"prompt,omitempty"`
	PromptTruncated              bool                `json:"prompt_truncated,omitempty"`
	PromptFile                   string              `json:"prompt_file,omitempty"`
	Priority                     int                 `json:"priority"`
	QueueSequence                int64               `json:"queue_sequence"`
	ExecutionProfileID           string              `json:"execution_profile_id"`
	RuntimeJobID                 string              `json:"runtime_job_id,omitempty"`
	Type                         domain.TaskType     `json:"type"`
	DispatchTier                 domain.DispatchTier `json:"dispatch_tier"`
	MinInterval                  string              `json:"min_interval"`
	RequireRepoChange            bool                `json:"require_repo_change"`
	Enabled                      bool                `json:"enabled"`
	State                        domain.TaskState    `json:"state"`
	LastStartedAt                *time.Time          `json:"last_started_at,omitempty"`
	LastCompletedAt              *time.Time          `json:"last_completed_at,omitempty"`
	LastSuccessfulSourceRevision string              `json:"last_successful_source_revision,omitempty"`
}

type runSummary struct {
	ID                string          `json:"id"`
	TaskID            string          `json:"task_id"`
	ProviderAccountID string          `json:"provider_account_id"`
	State             domain.RunState `json:"state"`
	StartedAt         time.Time       `json:"started_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	Error             string          `json:"error,omitempty"`
	FinalizeState     string          `json:"finalize_state,omitempty"`
	FinalizeError     string          `json:"finalize_error,omitempty"`
}

type runEventView struct {
	ID               int64     `json:"id"`
	RunID            string    `json:"run_id"`
	Type             string    `json:"type"`
	OccurredAt       time.Time `json:"occurred_at"`
	Payload          any       `json:"payload"`
	PayloadBytes     int       `json:"payload_bytes"`
	PayloadTruncated bool      `json:"payload_truncated,omitempty"`
}

// New builds an MCP server backed entirely by the supplied Redline API client.
func New(client apiclient.Client) *mcp.Server {
	s := &server{client: client}
	result := mcp.NewServer(&mcp.Implementation{
		Name:    "redline",
		Title:   "Redline budget-aware task scheduler",
		Version: "0.1.0",
	}, nil)

	mcp.AddTool(result, readTool("redline_overview", "Redline overview",
		"Get compact operational health, provider usage, scheduler state, queued tasks, and recent runs."), s.overview)
	mcp.AddTool(result, readTool("redline_provider_status", "Provider usage status",
		"Get the latest usage snapshot and reset windows for one configured provider account."), s.providerStatus)
	mcp.AddTool(result, readTool("redline_provider_capacity", "Provider capacity estimate",
		"Get empirical token-capacity and allowance-calibration evidence for one provider."), s.providerCapacity)
	mcp.AddTool(result, readTool("redline_tasks_list", "List tasks",
		"List queued, running, completed, failed, and disabled Redline tasks with a bounded response."), s.tasksList)
	mcp.AddTool(result, readTool("redline_task_get", "Get task",
		"Get one Redline task definition by ID."), s.taskGet)
	mcp.AddTool(result, readTool("redline_profiles_list", "List execution profiles",
		"List the configured harness, model, repository, and workspace execution profiles."), s.profilesList)
	mcp.AddTool(result, readTool("redline_profile_get", "Get execution profile",
		"Get one execution profile, including harness, model, repository, workspace, and lifecycle hooks."), s.profileGet)
	mcp.AddTool(result, readTool("redline_runtime_connections_list", "List runtime connections",
		"List configured remote runtime connections without exposing credential contents."), s.runtimeConnectionsList)
	mcp.AddTool(result, readTool("redline_runtime_connection_get", "Get runtime connection",
		"Get one runtime connection and its credential reference, never the referenced secret."), s.runtimeConnectionGet)
	mcp.AddTool(result, readTool("redline_agent_contexts_list", "List agent contexts",
		"List configured runtime profiles, projects, and remote working directories."), s.agentContextsList)
	mcp.AddTool(result, readTool("redline_agent_context_get", "Get agent context",
		"Get one configured remote agent context."), s.agentContextGet)
	mcp.AddTool(result, readTool("redline_runs_list", "List runs",
		"List recent Redline run lifecycles with a bounded response."), s.runsList)
	mcp.AddTool(result, readTool("redline_run_get", "Get run",
		"Get one run, including workspace, lifecycle state, exit code, and errors."), s.runGet)
	mcp.AddTool(result, readTool("redline_run_events", "Get run events",
		"Get a bounded lifecycle audit trail for a run."), s.runEvents)
	mcp.AddTool(result, readTool("redline_run_logs", "Read run log tail",
		"Read a bounded tail of one run log stream; never returns more than 32 KiB."), s.runLogs)

	mcp.AddTool(result, mutationTool("redline_task_create", "Create task",
		"Create a queued one-off or recurring task through the Redline service API.", false, false, false), s.taskCreate)
	mcp.AddTool(result, mutationTool("redline_task_update", "Update task",
		"Update scheduling or instruction fields on an existing task.", true, true, false), s.taskUpdate)
	mcp.AddTool(result, mutationTool("redline_task_control", "Control task",
		"Enable, disable, or retry an existing task.", false, false, false), s.taskControl)
	mcp.AddTool(result, mutationTool("redline_profile_create", "Create execution profile",
		"Create a harness, model, workspace, and lifecycle-hook execution profile.", false, false, false), s.profileCreate)
	mcp.AddTool(result, mutationTool("redline_profile_update", "Update execution profile",
		"Update harness, model, workspace, repository, or lifecycle-hook fields on an execution profile.", true, true, false), s.profileUpdate)
	mcp.AddTool(result, mutationTool("redline_profile_delete", "Delete execution profile",
		"Delete an execution profile. The service rejects deletion while a task references it.", true, false, false), s.profileDelete)
	mcp.AddTool(result, mutationTool("redline_runtime_connection_create", "Create runtime connection",
		"Create a Hermes runtime connection using only a credential reference, never an inline secret.", false, false, true), s.runtimeConnectionCreate)
	mcp.AddTool(result, mutationTool("redline_runtime_connection_update", "Update runtime connection",
		"Update a runtime endpoint, credential reference, or concurrency setting.", true, true, true), s.runtimeConnectionUpdate)
	mcp.AddTool(result, mutationTool("redline_runtime_connection_delete", "Delete runtime connection",
		"Delete an unreferenced runtime connection.", true, false, false), s.runtimeConnectionDelete)
	mcp.AddTool(result, mutationTool("redline_runtime_connection_discover", "Discover runtime options",
		"Connect to a runtime and return compact profiles, projects, and providers; optionally filter and page model identifiers.", false, false, true), s.runtimeConnectionDiscover)
	mcp.AddTool(result, mutationTool("redline_agent_context_create", "Create agent context",
		"Create a runtime profile, project, and working-directory selection.", false, false, false), s.agentContextCreate)
	mcp.AddTool(result, mutationTool("redline_agent_context_update", "Update agent context",
		"Update a runtime profile, project, working directory, session mode, or concurrency setting.", true, true, false), s.agentContextUpdate)
	mcp.AddTool(result, mutationTool("redline_agent_context_delete", "Delete agent context",
		"Delete an agent context that is not referenced by an execution profile.", true, false, false), s.agentContextDelete)
	mcp.AddTool(result, mutationTool("redline_provider_control", "Control provider",
		"Pause or resume automatic dispatch for a configured provider account.", false, true, false), s.providerControl)
	mcp.AddTool(result, mutationTool("redline_provider_concurrency", "Set provider concurrency",
		"Set the provider parallel-run limit, or use zero to restore its configured default.", false, true, false), s.providerConcurrency)
	mcp.AddTool(result, mutationTool("redline_scheduler_evaluate", "Evaluate scheduler",
		"Evaluate current usage and task eligibility without launching a task.", false, false, false), s.schedulerEvaluate)
	mcp.AddTool(result, mutationTool("redline_scheduler_dispatch", "Dispatch scheduler task",
		"Evaluate and, when admitted, launch the highest-priority eligible task. The launched harness runs to completion.", true, false, true), s.schedulerDispatch)
	return result
}

// RunStdio serves Redline MCP over stdin/stdout until the client disconnects.
func RunStdio(ctx context.Context, client apiclient.Client) error {
	return New(client).Run(ctx, &mcp.StdioTransport{})
}

func (s *server) overview(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, Output, error) {
	var dashboard struct {
		GeneratedAt  time.Time                `json:"generated_at"`
		ActivePolicy string                   `json:"active_policy"`
		Health       domain.OperationalHealth `json:"health"`
		Scheduler    any                      `json:"scheduler"`
		UsageMonitor any                      `json:"usage_monitor"`
		Providers    []map[string]any         `json:"providers"`
		Tasks        []map[string]any         `json:"tasks"`
		Runs         []domain.Run             `json:"runs"`
		Attempts     []domain.DispatchAttempt `json:"attempts"`
	}
	if err := s.client.Do(ctx, http.MethodGet, "/v1/dashboard", nil, &dashboard); err != nil {
		return nil, Output{}, err
	}
	providersTruncated := len(dashboard.Providers) > maxOverviewItems
	tasksTruncated := len(dashboard.Tasks) > maxOverviewItems
	dashboard.Providers = truncate(dashboard.Providers, maxOverviewItems)
	dashboard.Tasks = truncate(dashboard.Tasks, maxOverviewItems)
	dashboard.Runs = truncate(dashboard.Runs, 10)
	dashboard.Attempts = truncate(dashboard.Attempts, 10)
	recentRuns := make([]runSummary, 0, len(dashboard.Runs))
	for _, run := range dashboard.Runs {
		recentRuns = append(recentRuns, summarizeRun(run))
	}
	data := map[string]any{
		"generated_at": dashboard.GeneratedAt, "active_policy": dashboard.ActivePolicy,
		"health": dashboard.Health, "scheduler": dashboard.Scheduler, "usage_monitor": dashboard.UsageMonitor,
		"providers": dashboard.Providers, "providers_truncated": providersTruncated,
		"tasks": dashboard.Tasks, "tasks_truncated": tasksTruncated,
		"recent_runs": recentRuns, "recent_attempts": dashboard.Attempts,
	}
	summary := fmt.Sprintf("Redline is %s with %d provider(s), %d task(s), and %d active run(s).",
		dashboard.Health.Status, len(dashboard.Providers), len(dashboard.Tasks), dashboard.Health.ActiveRuns)
	return nil, Output{Summary: summary, Data: data}, nil
}

func (s *server) providerStatus(ctx context.Context, _ *mcp.CallToolRequest, input providerInput) (*mcp.CallToolResult, Output, error) {
	var snapshot decision.UsageSnapshot
	if err := s.client.Do(ctx, http.MethodGet, providerPath(input.ProviderAccountID, "status"), nil, &snapshot); err != nil {
		return nil, Output{}, err
	}
	short := "no short-window limit"
	if snapshot.Short != nil {
		short = fmt.Sprintf("%.1f%% short-window remaining", snapshot.Short.Remaining*100)
	}
	summary := fmt.Sprintf("%s has %.1f%% weekly remaining and %s.",
		input.ProviderAccountID, snapshot.Weekly.Remaining*100, short)
	return nil, Output{Summary: summary, Data: snapshot}, nil
}

func (s *server) providerCapacity(ctx context.Context, _ *mcp.CallToolRequest, input providerInput) (*mcp.CallToolResult, Output, error) {
	var estimate capacity.EstimateResult
	if err := s.client.Do(ctx, http.MethodGet, providerPath(input.ProviderAccountID, "capacity"), nil, &estimate); err != nil {
		return nil, Output{}, err
	}
	summary := fmt.Sprintf("%s capacity estimate confidence is %s from %d snapshots and %d token observations.",
		input.ProviderAccountID, estimate.Confidence, estimate.SnapshotCount, estimate.TokenObservationCount)
	return nil, Output{Summary: summary, Data: estimate}, nil
}

func (s *server) tasksList(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, Output, error) {
	var items []domain.Task
	if err := s.client.Do(ctx, http.MethodGet, "/v1/tasks", nil, &items); err != nil {
		return nil, Output{}, err
	}
	views := make([]taskView, 0, len(items))
	for _, item := range items {
		views = append(views, viewTask(item, false))
	}
	return listOutput("task", views, input.Limit)
}

func (s *server) taskGet(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	var item domain.Task
	if err := s.client.Do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(input.ID), nil, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Task %s is %s.", item.ID, item.State), Data: viewTask(item, true)}, nil
}

func (s *server) profilesList(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, Output, error) {
	var items []domain.ExecutionProfile
	if err := s.client.Do(ctx, http.MethodGet, "/v1/profiles", nil, &items); err != nil {
		return nil, Output{}, err
	}
	return listOutput("execution profile", items, input.Limit)
}

func (s *server) profileGet(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	var item domain.ExecutionProfile
	if err := s.client.Do(ctx, http.MethodGet, "/v1/profiles/"+url.PathEscape(input.ID), nil, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Execution profile %s uses %s with %s.",
		item.ID, item.HarnessType, item.WorkspaceProvider), Data: item}, nil
}

func (s *server) runsList(ctx context.Context, _ *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, Output, error) {
	var items []domain.Run
	if err := s.client.Do(ctx, http.MethodGet, "/v1/runs", nil, &items); err != nil {
		return nil, Output{}, err
	}
	return listOutput("run", items, input.Limit)
}

func (s *server) runGet(ctx context.Context, _ *mcp.CallToolRequest, input runInput) (*mcp.CallToolResult, Output, error) {
	var item domain.Run
	if err := s.client.Do(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(input.RunID), nil, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Run %s is %s.", item.ID, item.State), Data: item}, nil
}

func (s *server) runEvents(ctx context.Context, _ *mcp.CallToolRequest, input runEventsInput) (*mcp.CallToolResult, Output, error) {
	limit := boundedLimit(input.Limit)
	var items []domain.RunEvent
	path := "/v1/runs/" + url.PathEscape(input.RunID) + "/events?limit=" + strconv.Itoa(limit+1)
	if err := s.client.Do(ctx, http.MethodGet, path, nil, &items); err != nil {
		return nil, Output{}, err
	}
	total := len(items)
	if total > limit {
		// The API already returns only the most recent limit+1 rows, oldest
		// first; drop the oldest of those to keep the most recent `limit`.
		items = items[total-limit:]
	}
	views := make([]runEventView, 0, len(items))
	for _, item := range items {
		views = append(views, viewRunEvent(item))
	}
	return nil, Output{Summary: fmt.Sprintf("Returned %d event(s) for run %s.", len(items), input.RunID),
		Count: len(items), Truncated: total > len(items), Data: views}, nil
}

func (s *server) runLogs(ctx context.Context, _ *mcp.CallToolRequest, input runLogsInput) (*mcp.CallToolResult, Output, error) {
	stream := input.Stream
	if stream == "" {
		stream = "stdout"
	}
	if !validLogStream(stream) {
		return nil, Output{}, fmt.Errorf("unsupported log stream %q", stream)
	}
	tailBytes := input.TailByte
	if tailBytes <= 0 {
		tailBytes = defaultLogTail
	}
	if tailBytes > maxLogTail {
		tailBytes = maxLogTail
	}
	path := "/v1/runs/" + url.PathEscape(input.RunID) + "/logs?stream=" +
		url.QueryEscape(stream) + "&tail_bytes=" + strconv.FormatInt(tailBytes, 10)
	var tail artifacts.Tail
	if err := s.client.Do(ctx, http.MethodGet, path, nil, &tail); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Returned a bounded %s tail for run %s.", stream, input.RunID),
		Data: map[string]any{"run_id": input.RunID, "stream": stream, "tail": tail}}, nil
}

func (s *server) taskCreate(ctx context.Context, _ *mcp.CallToolRequest, input taskCreateInput) (*mcp.CallToolResult, Output, error) {
	var item domain.Task
	if err := s.client.Do(ctx, http.MethodPost, "/v1/tasks", input, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Created queued task %s.", item.ID), Data: viewTask(item, true)}, nil
}

func (s *server) taskUpdate(ctx context.Context, _ *mcp.CallToolRequest, input taskUpdateInput) (*mcp.CallToolResult, Output, error) {
	body := map[string]any{}
	putOptional(body, "name", input.Name)
	putOptional(body, "prompt", input.Prompt)
	putOptional(body, "prompt_file", input.PromptFile)
	putOptional(body, "priority", input.Priority)
	putOptional(body, "execution_profile_id", input.ExecutionProfileID)
	putOptional(body, "runtime_job_id", input.RuntimeJobID)
	putOptional(body, "type", input.Type)
	putOptional(body, "dispatch_tier", input.DispatchTier)
	putOptional(body, "min_interval", input.MinInterval)
	putOptional(body, "require_repo_change", input.RequireRepoChange)
	var item domain.Task
	if err := s.client.Do(ctx, http.MethodPatch, "/v1/tasks/"+url.PathEscape(input.ID), body, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Updated task %s.", item.ID), Data: viewTask(item, true)}, nil
}

func (s *server) taskControl(ctx context.Context, _ *mcp.CallToolRequest, input taskControlInput) (*mcp.CallToolResult, Output, error) {
	if !oneOf(input.Control, "enable", "disable", "retry") {
		return nil, Output{}, fmt.Errorf("control must be enable, disable, or retry")
	}
	var item domain.Task
	path := "/v1/tasks/" + url.PathEscape(input.ID) + "/" + input.Control
	if err := s.client.Do(ctx, http.MethodPost, path, map[string]any{}, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("%s applied to task %s.", input.Control, item.ID), Data: viewTask(item, false)}, nil
}

func (s *server) profileCreate(ctx context.Context, _ *mcp.CallToolRequest, input profileCreateInput) (*mcp.CallToolResult, Output, error) {
	var item domain.ExecutionProfile
	if err := s.client.Do(ctx, http.MethodPost, "/v1/profiles", input, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Created execution profile %s.", item.ID), Data: item}, nil
}

func (s *server) profileUpdate(ctx context.Context, _ *mcp.CallToolRequest, input profileUpdateInput) (*mcp.CallToolResult, Output, error) {
	body := map[string]any{}
	putOptional(body, "provider_account_id", input.ProviderAccountID)
	putOptional(body, "agent_context_id", input.AgentContextID)
	putOptional(body, "harness_type", input.HarnessType)
	putOptional(body, "model", input.Model)
	putOptional(body, "budget_model_group", input.BudgetModelGroup)
	putOptional(body, "harness_command", input.HarnessCommand)
	putOptional(body, "harness_args", input.HarnessArgs)
	putOptional(body, "workspace_provider", input.WorkspaceProvider)
	putOptional(body, "workspace_args", input.WorkspaceArgs)
	putOptional(body, "repository", input.Repository)
	putOptional(body, "base_branch", input.BaseBranch)
	putOptional(body, "require_clean", input.RequireClean)
	putOptional(body, "cleanup_policy", input.CleanupPolicy)
	putOptional(body, "prepare_command", input.PrepareCommand)
	putOptional(body, "finalize_command", input.FinalizeCommand)
	var item domain.ExecutionProfile
	if err := s.client.Do(ctx, http.MethodPatch, "/v1/profiles/"+url.PathEscape(input.ID), body, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Updated execution profile %s.", item.ID), Data: item}, nil
}

func (s *server) runtimeConnectionsList(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, Output, error) {
	var items []domain.RuntimeConnection
	if err := s.client.Do(ctx, http.MethodGet, "/v1/runtime-connections", nil, &items); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Found %d runtime connections.", len(items)), Count: len(items), Data: items}, nil
}

func (s *server) runtimeConnectionGet(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	var item domain.RuntimeConnection
	if err := s.client.Do(ctx, http.MethodGet, "/v1/runtime-connections/"+url.PathEscape(input.ID), nil, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Loaded runtime connection %s.", item.ID), Data: item}, nil
}

func (s *server) runtimeConnectionCreate(ctx context.Context, _ *mcp.CallToolRequest, input runtimeConnectionCreateInput) (*mcp.CallToolResult, Output, error) {
	var item domain.RuntimeConnection
	if err := s.client.Do(ctx, http.MethodPost, "/v1/runtime-connections", input, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Created runtime connection %s.", item.ID), Data: item}, nil
}

func (s *server) runtimeConnectionUpdate(ctx context.Context, _ *mcp.CallToolRequest, input runtimeConnectionUpdateInput) (*mcp.CallToolResult, Output, error) {
	body := map[string]any{}
	putOptional(body, "runtime", input.Runtime)
	putOptional(body, "transport", input.Transport)
	putOptional(body, "url", input.URL)
	putOptional(body, "credential_source", input.CredentialSource)
	putOptional(body, "credential_ref", input.CredentialRef)
	putOptional(body, "desktop_config_path", input.DesktopConfigPath)
	putOptional(body, "max_concurrent_runs", input.MaxConcurrentRuns)
	var item domain.RuntimeConnection
	if err := s.client.Do(ctx, http.MethodPatch, "/v1/runtime-connections/"+url.PathEscape(input.ID), body, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Updated runtime connection %s.", item.ID), Data: item}, nil
}

func (s *server) runtimeConnectionDelete(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	if err := s.client.Do(ctx, http.MethodDelete, "/v1/runtime-connections/"+url.PathEscape(input.ID), nil, nil); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Deleted runtime connection %s.", input.ID), Data: map[string]any{"id": input.ID, "deleted": true}}, nil
}

func (s *server) runtimeConnectionDiscover(ctx context.Context, _ *mcp.CallToolRequest, input runtimeConnectionDiscoverInput) (*mcp.CallToolResult, Output, error) {
	body := hermes.DiscoveryOptions{
		Profile: input.Profile, Provider: input.Provider, IncludeModels: input.IncludeModels,
		ModelOffset: input.ModelOffset, ModelLimit: input.ModelLimit,
	}
	var result hermes.Discovery
	if err := s.client.Do(ctx, http.MethodPost, "/v1/runtime-connections/"+url.PathEscape(input.ID)+"/discover", body, &result); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{
		Summary:   fmt.Sprintf("Discovered runtime connection %s.", input.ID),
		Truncated: result.Truncated, Data: result,
	}, nil
}

func (s *server) agentContextsList(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, Output, error) {
	var items []domain.AgentContext
	if err := s.client.Do(ctx, http.MethodGet, "/v1/agent-contexts", nil, &items); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Found %d agent contexts.", len(items)), Count: len(items), Data: items}, nil
}

func (s *server) agentContextGet(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	var item domain.AgentContext
	if err := s.client.Do(ctx, http.MethodGet, "/v1/agent-contexts/"+url.PathEscape(input.ID), nil, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Loaded agent context %s.", item.ID), Data: item}, nil
}

func (s *server) agentContextCreate(ctx context.Context, _ *mcp.CallToolRequest, input agentContextCreateInput) (*mcp.CallToolResult, Output, error) {
	var item domain.AgentContext
	if err := s.client.Do(ctx, http.MethodPost, "/v1/agent-contexts", input, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Created agent context %s.", item.ID), Data: item}, nil
}

func (s *server) agentContextUpdate(ctx context.Context, _ *mcp.CallToolRequest, input agentContextUpdateInput) (*mcp.CallToolResult, Output, error) {
	body := map[string]any{}
	putOptional(body, "runtime_connection_id", input.RuntimeConnectionID)
	putOptional(body, "profile", input.Profile)
	putOptional(body, "agent", input.Agent)
	putOptional(body, "project", input.Project)
	putOptional(body, "working_directory", input.WorkingDirectory)
	putOptional(body, "session_mode", input.SessionMode)
	putOptional(body, "max_concurrent_runs", input.MaxConcurrentRuns)
	var item domain.AgentContext
	if err := s.client.Do(ctx, http.MethodPatch, "/v1/agent-contexts/"+url.PathEscape(input.ID), body, &item); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Updated agent context %s.", item.ID), Data: item}, nil
}

func (s *server) agentContextDelete(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	if err := s.client.Do(ctx, http.MethodDelete, "/v1/agent-contexts/"+url.PathEscape(input.ID), nil, nil); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Deleted agent context %s.", input.ID), Data: map[string]any{"id": input.ID, "deleted": true}}, nil
}

func (s *server) profileDelete(ctx context.Context, _ *mcp.CallToolRequest, input idInput) (*mcp.CallToolResult, Output, error) {
	if err := s.client.Do(ctx, http.MethodDelete, "/v1/profiles/"+url.PathEscape(input.ID), nil, nil); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("Deleted execution profile %s.", input.ID),
		Data: map[string]any{"id": input.ID, "deleted": true}}, nil
}

func (s *server) providerControl(ctx context.Context, _ *mcp.CallToolRequest, input providerControlInput) (*mcp.CallToolResult, Output, error) {
	if !oneOf(input.Control, "pause", "resume") {
		return nil, Output{}, fmt.Errorf("control must be pause or resume")
	}
	var response map[string]any
	if err := s.client.Do(ctx, http.MethodPost, providerPath(input.ProviderAccountID, input.Control), map[string]any{}, &response); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("%s applied to provider %s.", input.Control, input.ProviderAccountID), Data: response}, nil
}

func (s *server) providerConcurrency(ctx context.Context, _ *mcp.CallToolRequest, input providerConcurrencyInput) (*mcp.CallToolResult, Output, error) {
	var result map[string]any
	if err := s.client.Do(ctx, http.MethodPatch,
		providerPath(input.ProviderAccountID, "concurrency"),
		map[string]any{"max_concurrent_runs": input.MaxConcurrentRuns}, &result); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{
		Summary: fmt.Sprintf("Updated provider concurrency for %s.", input.ProviderAccountID),
		Data:    result,
	}, nil
}

func (s *server) schedulerEvaluate(ctx context.Context, _ *mcp.CallToolRequest, input schedulerInput) (*mcp.CallToolResult, Output, error) {
	return s.scheduler(ctx, "/v1/scheduler/evaluate", input, "Evaluated")
}

func (s *server) schedulerDispatch(ctx context.Context, _ *mcp.CallToolRequest, input schedulerInput) (*mcp.CallToolResult, Output, error) {
	return s.scheduler(ctx, "/v1/scheduler/execute", input, "Dispatched")
}

func (s *server) scheduler(ctx context.Context, path string, input schedulerInput, verb string) (*mcp.CallToolResult, Output, error) {
	var response map[string]any
	body := map[string]string{"provider_account_id": input.ProviderAccountID, "current_revision": input.CurrentRevision}
	if err := s.client.Do(ctx, http.MethodPost, path, body, &response); err != nil {
		return nil, Output{}, err
	}
	return nil, Output{Summary: fmt.Sprintf("%s scheduler for %s.", verb, input.ProviderAccountID), Data: response}, nil
}

func readTool(name, title, description string) *mcp.Tool {
	openWorld := false
	destructive := false
	return &mcp.Tool{Name: name, Title: title, Description: description, Annotations: &mcp.ToolAnnotations{
		Title: title, ReadOnlyHint: true, DestructiveHint: &destructive, OpenWorldHint: &openWorld,
	}}
}

func mutationTool(name, title, description string, destructive, idempotent, openWorld bool) *mcp.Tool {
	return &mcp.Tool{Name: name, Title: title, Description: description, Annotations: &mcp.ToolAnnotations{
		Title: title, ReadOnlyHint: false, DestructiveHint: &destructive,
		IdempotentHint: idempotent, OpenWorldHint: &openWorld,
	}}
}

func providerPath(provider, action string) string {
	return "/v1/providers/" + url.PathEscape(provider) + "/" + action
}

func boundedLimit(requested int) int {
	if requested <= 0 {
		return defaultListLimit
	}
	if requested > maxListLimit {
		return maxListLimit
	}
	return requested
}

func truncate[T any](items []T, limit int) []T {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func listOutput[T any](kind string, items []T, requested int) (*mcp.CallToolResult, Output, error) {
	limit := boundedLimit(requested)
	total := len(items)
	items = truncate(items, limit)
	return nil, Output{
		Summary: fmt.Sprintf("Returned %d of %d %s(s).", len(items), total, kind),
		Count:   len(items), Truncated: len(items) < total, Data: items,
	}, nil
}

func validLogStream(stream string) bool {
	return oneOf(stream, "stdout", "stderr", "prepare_stdout", "prepare_stderr", "finalize_stdout", "finalize_stderr")
}

func oneOf(value string, choices ...string) bool {
	return slicesContains(choices, value)
}

func slicesContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func putOptional[T any](output map[string]any, key string, value *T) {
	if value != nil {
		output[key] = *value
	}
}

func viewTask(item domain.Task, includePrompt bool) taskView {
	result := taskView{
		ID: item.ID, Name: item.Name, PromptFile: item.PromptFile, Priority: item.Priority,
		QueueSequence: item.QueueSequence, ExecutionProfileID: item.ExecutionProfileID,
		RuntimeJobID: item.RuntimeJobID,
		Type:         item.Type, DispatchTier: item.DispatchTier, MinInterval: item.MinInterval.String(),
		RequireRepoChange: item.RequireRepoChange, Enabled: item.Enabled, State: item.State,
		LastStartedAt: item.LastStartedAt, LastCompletedAt: item.LastCompletedAt,
		LastSuccessfulSourceRevision: item.LastSuccessfulSourceRevision,
	}
	if includePrompt {
		result.Prompt, result.PromptTruncated = truncateString(item.Prompt, maxPromptBytes)
	}
	return result
}

func summarizeRun(run domain.Run) runSummary {
	return runSummary{
		ID: run.ID, TaskID: run.TaskID, ProviderAccountID: run.ProviderAccountID,
		State: run.State, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		ExitCode: run.ExitCode, Error: run.Error, FinalizeState: run.FinalizeState,
		FinalizeError: run.FinalizeError,
	}
}

func viewRunEvent(event domain.RunEvent) runEventView {
	view := runEventView{
		ID: event.ID, RunID: event.RunID, Type: event.Type,
		OccurredAt: event.OccurredAt, PayloadBytes: len(event.Payload),
	}
	if len(event.Payload) > maxEventPayload {
		view.Payload, view.PayloadTruncated = truncateString(string(event.Payload), maxEventPayload)
		return view
	}
	if err := json.Unmarshal(event.Payload, &view.Payload); err != nil {
		view.Payload = string(event.Payload)
	}
	return view
}

func truncateString(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit], true
}
