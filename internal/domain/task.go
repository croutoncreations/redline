package domain

import (
	"encoding/json"
	"time"
)

type TaskType string
type TaskState string

const (
	OneOff    TaskType = "one_off"
	Recurring TaskType = "recurring"

	Queued    TaskState = "queued"
	Running   TaskState = "running"
	Completed TaskState = "completed"
	Failed    TaskState = "failed"
	Disabled  TaskState = "disabled"
)

type ExecutionProfile struct {
	ID                string    `json:"id"`
	ProviderAccountID string    `json:"provider_account_id"`
	HarnessType       string    `json:"harness_type"`
	Model             string    `json:"model,omitempty"`
	HarnessCommand    string    `json:"harness_command,omitempty"`
	HarnessArgs       []string  `json:"harness_args,omitempty"`
	WorkspaceProvider string    `json:"workspace_provider"`
	Repository        string    `json:"repository,omitempty"`
	BaseBranch        string    `json:"base_branch,omitempty"`
	RequireClean      bool      `json:"require_clean"`
	CleanupPolicy     string    `json:"cleanup_policy,omitempty"`
	PrepareCommand    string    `json:"prepare_command,omitempty"`
	FinalizeCommand   string    `json:"finalize_command,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
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
	Type                         TaskType      `json:"type"`
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

type RunState string

const (
	RunPreparing RunState = "preparing"
	RunRunning   RunState = "running"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
)

type Run struct {
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id"`
	ProviderAccountID string     `json:"provider_account_id"`
	State             RunState   `json:"state"`
	Workspace         Workspace  `json:"workspace"`
	SourceRevision    string     `json:"source_revision,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	ExitCode          *int       `json:"exit_code,omitempty"`
	OutputFile        string     `json:"output_file,omitempty"`
	ErrorFile         string     `json:"error_file,omitempty"`
	Error             string     `json:"error,omitempty"`
	FinalizeState     string     `json:"finalize_state,omitempty"`
	FinalizeError     string     `json:"finalize_error,omitempty"`
}

type RunCompletion struct {
	State         RunState
	ExitCode      int
	OutputFile    string
	ErrorFile     string
	Error         string
	FinalizeState string
	FinalizeError string
}
