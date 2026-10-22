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

// Evaluate runs all green-lane rules against the PR and returns the result
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

	for _, issuer := range e.config.ProvenanceIssuers {
		result.ProvenanceApps = append(result.ProvenanceApps, issuer.AppID)
	}
	result.Rules = append(result.Rules, e.checkPRState(pr), e.checkReviewReadiness(pr))
	lane := selectLane(pr)
	result.Lane = lane

	// Rule 1: Size check, with the lane's caps
	sizeResult := e.checkSize(pr, e.laneLimits(lane))
	result.Rules = append(result.Rules, sizeResult)

	// Rule 2: the code lane needs a fails-before/passes-after repro; the
	// docs/deps lane has no tests to run and instead restricts what its
	// manifest edits may contain.
	if lane == LaneDocsDeps {
		result.Rules = append(result.Rules, e.checkDocsDepsFiles(pr))
	} else {
		if reproResult != nil && (reproResult.HeadSHA != pr.HeadSHA || reproResult.BaseSHA != pr.BaseSHA) {
			result.Rules = append(result.Rules, RuleResult{Name: "Test Repro", Inconclusive: true, Message: "Repro evidence does not match the evaluated head/base snapshot"})
		} else {
			result.Rules = append(result.Rules, e.checkRepro(reproResult))
		}
	}

	// Rule 3: CI status
	ciResult := e.checkCI(pr)
	result.Rules = append(result.Rules, ciResult)

	// Rule 4: Cross-vendor review
	reviewResult := e.checkReviewer(pr)
	result.Rules = append(result.Rules, reviewResult)

	// Rule 5: No duplicate PRs
	dupeResult := e.checkDuplicates(pr, openPRs)
	result.Rules = append(result.Rules, dupeResult)

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
			if r.Name != "Reviewer" {
				result.AdvisoryCandidate = false
			}
			reasons = append(reasons, fmt.Sprintf("%s: %s", r.Name, r.Message))
		}
	}

	if result.AdvisoryCandidate {
		result.CandidateMessage = "Advisory only: all non-provenance rules pass; live gate and integration freshness are not implemented"
	} else {
		result.CandidateMessage = "Not a candidate: one or more non-provenance rules are blocked"
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
