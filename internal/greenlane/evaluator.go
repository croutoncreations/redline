package greenlane

import (
	"fmt"
	"time"
)

// Evaluator performs green-lane rule evaluation
type Evaluator struct {
	config Config
}

// NewEvaluator creates a new evaluator with the given configuration
func NewEvaluator(config Config) *Evaluator {
	return &Evaluator{config: config}
}

// Evaluate runs the simple policy. The legacy repro/duplicate arguments are
// retained for callers but no longer affect eligibility.
func (e *Evaluator) Evaluate(pr PRInfo, reproResult *ReproResult, openPRs []PRSummary) EvaluationResult {
	result := EvaluationResult{
		ReviewSnapshot: ReviewSnapshot(pr),
		CheckSnapshot:  CheckSnapshot(pr.CheckEvidence), CheckSources: e.config.CheckSources,
		Repository: pr.Repository, BaseBranch: pr.BaseBranch,
		PRNumber:    pr.Number,
		HeadSHA:     pr.HeadSHA,
		BaseSHA:     pr.BaseSHA,
		UpdatedAt:   pr.UpdatedAt,
		EvaluatedAt: time.Now().UTC(),
		Rules:       make([]RuleResult, 0),
	}

	result.Rules = append(result.Rules, e.checkPRState(pr), e.checkAuthor(pr), e.checkReviewReadiness(pr))
	lane := selectLane(pr)
	result.Lane = lane

	// Rule 1: Size check, with the lane's caps
	sizeResult := e.checkSize(pr, e.laneLimits(lane))
	result.Rules = append(result.Rules, sizeResult)

	// Dependency syntax/lockfile validation remains conservative. Ordinary
	// bug fixes rely on required CI; a new fails-before test is not mandatory.
	if lane == LaneDocsDeps {
		result.Rules = append(result.Rules, e.checkDocsDepsFiles(pr))
	}

	// Rule 3: CI status
	ciResult := e.checkCI(pr)
	result.Rules = append(result.Rules, ciResult)

	// Rule 6: Denied paths
	pathResult := e.checkDeniedPaths(pr)
	result.Rules = append(result.Rules, pathResult)

	// Determine verdict
	allPass := true
	result.AdvisoryCandidate = true
	var reasons []string

	for _, r := range result.Rules {
		if !r.Pass {
			allPass = false
			result.AdvisoryCandidate = false
			reasons = append(reasons, fmt.Sprintf("%s: %s", r.Name, r.Message))
		}
	}

	if result.AdvisoryCandidate {
		result.CandidateMessage = "Advisory only: all policy rules pass; shadow mode never authorizes a merge"
	} else {
		result.CandidateMessage = "Not a candidate: one or more policy rules are blocked"
	}
	if allPass {
		result.Verdict = VerdictWouldMerge
		result.Summary = fmt.Sprintf("All %s-lane rules pass. Shadow mode: would merge if live.", lane)
	} else {
		result.Verdict = VerdictWouldNotMerge
		result.Summary = fmt.Sprintf("Would not merge (%s lane). Reasons:\n- %s",
			lane, join(reasons, "\n- "))
	}

	return result
}

// join is a simple helper to join strings (avoiding external dependencies)
func join(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for i := 1; i < len(items); i++ {
		result += sep + items[i]
	}
	return result
}

// ReproResult represents the outcome of the test repro job
type ReproResult struct {
	HeadSHA      string
	BaseSHA      string
	Pass         bool
	Inconclusive bool
	Message      string
	TestIDs      []string
	BaseRun      *TestRunResult
	HeadRun      *TestRunResult
}

// TestRunResult represents the result of running tests
type TestRunResult struct {
	TestResults map[string]TestStatus
	CompileOK   bool
}

// TestStatus represents the status of a single test
type TestStatus struct {
	Executed bool
	Passed   bool
}

// PRSummary contains minimal information about an open PR for duplicate detection
type PRSummary struct {
	Number    int
	RootCause string
	Files     []string
}
