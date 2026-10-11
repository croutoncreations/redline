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
		PRNumber:    pr.Number,
		HeadSHA:     pr.HeadSHA,
		BaseSHA:     pr.BaseSHA,
		EvaluatedAt: time.Now().UTC(),
		Rules:       make([]RuleResult, 0),
	}

	// Rule 1: Size check
	sizeResult := e.checkSize(pr)
	result.Rules = append(result.Rules, sizeResult)

	// Rule 2: Test repro (fails before, passes after)
	reproCheckResult := e.checkRepro(reproResult)
	result.Rules = append(result.Rules, reproCheckResult)

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
	var reasons []string

	for _, r := range result.Rules {
		if !r.Pass {
			allPass = false
			reasons = append(reasons, fmt.Sprintf("%s: %s", r.Name, r.Message))
		}
	}

	if allPass {
		result.Verdict = VerdictWouldMerge
		result.Summary = "All rules pass. Shadow mode: would merge if live."
	} else {
		result.Verdict = VerdictWouldNotMerge
		result.Summary = fmt.Sprintf("Would not merge. Reasons:\n- %s",
			join(reasons, "\n- "))
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
