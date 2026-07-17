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
	WorkspaceProvider string    `json:"workspace_provider"`
	Repository        string    `json:"repository,omitempty"`
	BaseBranch        string    `json:"base_branch,omitempty"`
	PrepareCommand    string    `json:"prepare_command,omitempty"`
	FinalizeCommand   string    `json:"finalize_command,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
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
