package greenlane

import "time"

// Verdict represents the final evaluation result
type Verdict string

const (
	VerdictWouldMerge    Verdict = "would_merge"
	VerdictWouldNotMerge Verdict = "would_not_merge"
)

// RuleResult represents the outcome of evaluating a single rule
type RuleResult struct {
	Name         string `json:"name"`
	Pass         bool   `json:"pass"`
	Inconclusive bool   `json:"inconclusive,omitempty"`
	Message      string `json:"message"`
}

// EvaluationResult contains the full evaluation outcome
type EvaluationResult struct {
	Verdict     Verdict      `json:"verdict"`
	Rules       []RuleResult `json:"rules"`
	Summary     string       `json:"summary"`
	EvaluatedAt time.Time    `json:"evaluated_at"`
	PRNumber    int          `json:"pr_number"`
	HeadSHA     string       `json:"head_sha"`
	BaseSHA     string       `json:"base_sha"`
}

// PRFile represents a file changed in the PR
type PRFile struct {
	Filename  string
	Status    string
	Additions int
	Deletions int
	Changes   int
}

// CheckRun represents a GitHub check run status
type CheckRun struct {
	Name       string
	Status     string
	Conclusion string
	AppID      int64
}

// Review represents a GitHub PR review
type Review struct {
	ID          int64
	User        string
	State       string
	CommitID    string
	SubmittedAt time.Time
}

// ReviewComment represents an inline review comment on the diff
type ReviewComment struct {
	ID       int64
	ReviewID int64
	Path     string
	Line     int
	Body     string
}

// PRInfo contains information about the pull request
type PRInfo struct {
	Number         int
	Title          string
	Body           string
	HeadSHA        string
	BaseSHA        string
	BaseBranch     string
	Draft          bool
	Mergeable      bool
	Author         string
	Files          []PRFile
	CheckRuns      []CheckRun
	Reviews        []Review
	ReviewComments []ReviewComment
}

// Lane identifies which shadow evaluation lane a PR qualifies for.
type Lane string

const (
	// LaneDefault is the fails-before/repro lane for code changes.
	LaneDefault Lane = "default"
	// LaneDocsDeps is a shadow-only lane for docs, changelog, and dependency
	// PRs: no repro required, tighter size/file-type caps, same denied paths.
	LaneDocsDeps Lane = "docs_deps"
)

// Config represents the green-lane configuration
type Config struct {
	DeniedPaths        []string `yaml:"denied_paths"`
	MaxChangedLines    int      `yaml:"max_changed_lines"`
	MaxLockfileChanges int      `yaml:"max_lockfile_changes"`
	MaxFiles           int      `yaml:"max_files"`
	RequiredChecks     []string `yaml:"required_checks"`

	// Docs/deps lane caps (tighter than the default lane).
	DocsDepsMaxChangedLines    int `yaml:"docs_deps_max_changed_lines"`
	DocsDepsMaxLockfileChanges int `yaml:"docs_deps_max_lockfile_changes"`
	DocsDepsMaxFiles           int `yaml:"docs_deps_max_files"`
}
