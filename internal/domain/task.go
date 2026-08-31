package domain

import (
	"encoding/json"
	"time"
)

type TaskType string
type TaskState string
type DispatchTier string

const (
	OneOff    TaskType = "one_off"
	Recurring TaskType = "recurring"

	Queued    TaskState = "queued"
	Running   TaskState = "running"
	Completed TaskState = "completed"
	Failed    TaskState = "failed"
	Disabled  TaskState = "disabled"

	DispatchBehind     DispatchTier = "behind"
	DispatchWellBehind DispatchTier = "well_behind"
	DispatchExpiring   DispatchTier = "expiring"
)

type ExecutionProfile struct {
	ID                string    `json:"id"`
	ProviderAccountID string    `json:"provider_account_id"`
	AgentContextID    string    `json:"agent_context_id,omitempty"`
	HarnessType       string    `json:"harness_type"`
	Model             string    `json:"model,omitempty"`
	BudgetModelGroup  string    `json:"budget_model_group,omitempty"`
	HarnessCommand    string    `json:"harness_command,omitempty"`
	HarnessArgs       []string  `json:"harness_args,omitempty"`
	WorkspaceProvider string    `json:"workspace_provider"`
	WorkspaceArgs     []string  `json:"workspace_args,omitempty"`
	Repository        string    `json:"repository,omitempty"`
	BaseBranch        string    `json:"base_branch,omitempty"`
	RequireClean      bool      `json:"require_clean"`
	CleanupPolicy     string    `json:"cleanup_policy,omitempty"`
	PrepareCommand    string    `json:"prepare_command,omitempty"`
	FinalizeCommand   string    `json:"finalize_command,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type RuntimeConnection struct {
	ID                string    `json:"id"`
	Runtime           string    `json:"runtime"`
	Transport         string    `json:"transport"`
	URL               string    `json:"url,omitempty"`
	CredentialSource  string    `json:"credential_source,omitempty"`
	CredentialRef     string    `json:"credential_ref,omitempty"`
	DesktopConfigPath string    `json:"desktop_config_path,omitempty"`
	MaxConcurrentRuns int       `json:"max_concurrent_runs"`
	CreatedAt         time.Time `json:"created_at"`
}

type AgentContext struct {
	ID                  string    `json:"id"`
	RuntimeConnectionID string    `json:"runtime_connection_id"`
	Profile             string    `json:"profile,omitempty"`
	Agent               string    `json:"agent,omitempty"`
	Project             string    `json:"project,omitempty"`
	WorkingDirectory    string    `json:"working_directory,omitempty"`
	SessionMode         string    `json:"session_mode"`
	MaxConcurrentRuns   int       `json:"max_concurrent_runs"`
	CreatedAt           time.Time `json:"created_at"`
}

type ExternalRun struct {
	RuntimeConnectionID string `json:"runtime_connection_id,omitempty"`
	RunID               string `json:"run_id,omitempty"`
	SessionID           string `json:"session_id,omitempty"`
}

type Workspace struct {
	Directory string            `json:"directory"`
	Branch    string            `json:"branch,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type Task struct {
	ID                           string        `json:"id"`
	Name                         string        `json:"name"`
	Prompt                       string        `json:"prompt,omitempty"`
	PromptFile                   string        `json:"prompt_file,omitempty"`
	Priority                     int           `json:"priority"`
	QueueSequence                int64         `json:"queue_sequence"`
	ExecutionProfileID           string        `json:"execution_profile_id"`
	RuntimeJobID                 string        `json:"runtime_job_id,omitempty"`
	Type                         TaskType      `json:"type"`
	DispatchTier                 DispatchTier  `json:"dispatch_tier"`
	MinInterval                  time.Duration `json:"min_interval"`
	RequireRepoChange            bool          `json:"require_repo_change"`
	Enabled                      bool          `json:"enabled"`
	State                        TaskState     `json:"state"`
	LastStartedAt                *time.Time    `json:"last_started_at,omitempty"`
	LastCompletedAt              *time.Time    `json:"last_completed_at,omitempty"`
	LastSuccessfulSourceRevision string        `json:"last_successful_source_revision,omitempty"`
	CreatedAt                    time.Time     `json:"created_at"`
	UpdatedAt                    time.Time     `json:"updated_at"`
}

type SchedulerDecision struct {
	ID                int64           `json:"id"`
	ProviderAccountID string          `json:"provider_account_id"`
	DecisionJSON      json.RawMessage `json:"decision_json"`
	SelectedTaskID    string          `json:"selected_task_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type DispatchOutcome string

const (
	DispatchAdmitted DispatchOutcome = "admitted"
	DispatchWait     DispatchOutcome = "wait"
	DispatchNoTask   DispatchOutcome = "no_task"
	DispatchError    DispatchOutcome = "error"
)

type DispatchAttempt struct {
	ID                int64           `json:"id"`
	ProviderAccountID string          `json:"provider_account_id"`
	Trigger           string          `json:"trigger"`
	Outcome           DispatchOutcome `json:"outcome"`
	Decision          string          `json:"decision,omitempty"`
	Mode              string          `json:"mode,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	RequestedTaskID   string          `json:"requested_task_id,omitempty"`
	SelectedTaskID    string          `json:"selected_task_id,omitempty"`
	RunID             string          `json:"run_id,omitempty"`
	Error             string          `json:"error,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	CompletedAt       time.Time       `json:"completed_at"`
}

const (
	EventRunStarted     = "run.started"
	EventRunCompleted   = "run.completed"
	EventRunFailed      = "run.failed"
	EventSchedulerError = "scheduler.error"
)

type NotificationEvent struct {
	Version           int               `json:"version"`
	Type              string            `json:"type"`
	OccurredAt        time.Time         `json:"occurred_at"`
	ProviderAccountID string            `json:"provider_account_id,omitempty"`
	TaskID            string            `json:"task_id,omitempty"`
	RunID             string            `json:"run_id,omitempty"`
	Message           string            `json:"message"`
	Data              map[string]string `json:"data,omitempty"`
}

type NotificationDelivery struct {
	ID        int64           `json:"id"`
	EventType string          `json:"event_type"`
	Status    string          `json:"status"`
	Payload   json.RawMessage `json:"payload"`
	Attempts  int             `json:"attempts"`
	LastError string          `json:"last_error,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type OperationalHealth struct {
	Status               string    `json:"status"`
	Window               string    `json:"window"`
	Since                time.Time `json:"since"`
	ActiveRuns           int       `json:"active_runs"`
	CompletedRuns        int       `json:"completed_runs"`
	FailedRuns           int       `json:"failed_runs"`
	DispatchAttempts     int       `json:"dispatch_attempts"`
	DispatchErrors       int       `json:"dispatch_errors"`
	NotificationFailures int       `json:"notification_failures"`
}

type RunState string

const (
	RunPreparing RunState = "preparing"
	RunRunning   RunState = "running"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
)

type Run struct {
	ID                  string        `json:"id"`
	TaskID              string        `json:"task_id"`
	ProviderAccountID   string        `json:"provider_account_id"`
	RuntimeConnectionID string        `json:"runtime_connection_id,omitempty"`
	AgentContextID      string        `json:"agent_context_id,omitempty"`
	State               RunState      `json:"state"`
	Workspace           Workspace     `json:"workspace"`
	SourceRevision      string        `json:"source_revision,omitempty"`
	StartedAt           time.Time     `json:"started_at"`
	CompletedAt         *time.Time    `json:"completed_at,omitempty"`
	ExitCode            *int          `json:"exit_code,omitempty"`
	OutputFile          string        `json:"output_file,omitempty"`
	ErrorFile           string        `json:"error_file,omitempty"`
	Error               string        `json:"error,omitempty"`
	FinalizeState       string        `json:"finalize_state,omitempty"`
	FinalizeError       string        `json:"finalize_error,omitempty"`
	External            ExternalRun   `json:"external,omitempty"`
	Summary             string        `json:"summary,omitempty"`
	Outcome             string        `json:"outcome,omitempty"`
	Artifacts           []RunArtifact `json:"artifacts,omitempty"`
	Warnings            []string      `json:"warnings,omitempty"`
	ActualProvider      string        `json:"actual_provider,omitempty"`
	ActualModel         string        `json:"actual_model,omitempty"`
	ActivityReadAt      *time.Time    `json:"activity_read_at,omitempty"`
}

type RunArtifact struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
	Path  string `json:"path,omitempty"`
}

type RunCompletion struct {
	State          RunState
	ExitCode       int
	OutputFile     string
	ErrorFile      string
	Error          string
	FinalizeState  string
	FinalizeError  string
	Summary        string
	Outcome        string
	Artifacts      []RunArtifact
	Warnings       []string
	ActualProvider string
	ActualModel    string
}

type RunEvent struct {
	ID         int64           `json:"id"`
	RunID      string          `json:"run_id"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

const (
	RunEventStarted           = "run.started"
	RunEventWorkspacePrepare  = "workspace.prepare_started"
	RunEventWorkspacePrepared = "workspace.prepared"
	RunEventWorkspaceFailed   = "workspace.prepare_failed"
	RunEventHarnessStarted    = "harness.started"
	RunEventExternalStarted   = "runtime.external_started"
	RunEventHarnessCompleted  = "harness.completed"
	RunEventHarnessFailed     = "harness.failed"
	RunEventProviderPaused    = "provider.paused"
	RunEventUsageRecorded     = "usage.recorded"
	RunEventUsageFailed       = "usage.failed"
	RunEventFinalizeStarted   = "finalize.started"
	RunEventFinalizeCompleted = "finalize.completed"
	RunEventFinalizeFailed    = "finalize.failed"
	RunEventCleanupStarted    = "cleanup.started"
	RunEventCleanupCompleted  = "cleanup.completed"
	RunEventCleanupFailed     = "cleanup.failed"
	RunEventCompleted         = "run.completed"
	RunEventFailed            = "run.failed"
)
