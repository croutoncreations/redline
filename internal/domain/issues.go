package domain

import "time"

// IssueSource is user-owned configuration. Authentication stores references only.
type IssueSource struct {
	ID                 string       `json:"id"`
	Host               string       `json:"host"`
	Repository         string       `json:"repository"`
	Auth               string       `json:"auth"` // gh, public, environment
	Account            string       `json:"account,omitempty"`
	CredentialRef      string       `json:"credential_ref,omitempty"`
	Labels             []string     `json:"labels"`
	ExcludeLabels      []string     `json:"exclude_labels"`
	IncludeAssigned    bool         `json:"include_assigned"`
	ExecutionProfileID string       `json:"execution_profile_id"`
	Goal               string       `json:"goal"` // draft_pr, local_changes, investigation
	Instructions       string       `json:"instructions,omitempty"`
	VerifyCommand      string       `json:"verify_command,omitempty"`
	Priority           int          `json:"priority"`
	DispatchTier       DispatchTier `json:"dispatch_tier"`
	MaxQueued          int          `json:"max_queued"`
	Enabled            bool         `json:"enabled"`
	LastSyncedAt       *time.Time   `json:"last_synced_at,omitempty"`
	LastError          string       `json:"last_error,omitempty"`
	NextSyncAt         *time.Time   `json:"next_sync_at,omitempty"`
}

type IssueSnapshot struct {
	ID           int64     `json:"id"`
	RepositoryID int64     `json:"repository_id"`
	Number       int       `json:"number"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	State        string    `json:"state"`
	Labels       []string  `json:"labels"`
	Assignees    []string  `json:"assignees"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type IssueItem struct {
	ID       string         `json:"id"`
	SourceID string         `json:"source_id"`
	Host     string         `json:"host"`
	Snapshot IssueSnapshot  `json:"snapshot"`
	State    string         `json:"state"`
	Reason   string         `json:"reason,omitempty"`
	Attempts []IssueAttempt `json:"attempts"`
}

type IssueAttempt struct {
	ID            string           `json:"id"`
	ItemID        string           `json:"item_id"`
	TaskID        string           `json:"task_id"`
	RunID         string           `json:"run_id,omitempty"`
	Source        IssueSource      `json:"source"`
	Profile       ExecutionProfile `json:"profile"`
	Snapshot      IssueSnapshot    `json:"snapshot"`
	PromptVersion int              `json:"prompt_version"`
	State         string           `json:"state"`
	Summary       string           `json:"summary,omitempty"`
	Workspace     Workspace        `json:"workspace"`
	Commit        string           `json:"commit,omitempty"`
	PRURL         string           `json:"pr_url,omitempty"`
	Error         string           `json:"error,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type IssueCandidate struct {
	Issue    IssueSnapshot `json:"issue"`
	Eligible bool          `json:"eligible"`
	Reason   string        `json:"reason,omitempty"`
	ItemID   string        `json:"item_id,omitempty"`
}
