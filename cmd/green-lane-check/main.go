package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/greenlane"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Get inputs from environment
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN not set")
	}

	repoOwner := os.Getenv("REPO_OWNER")
	repoName := os.Getenv("REPO_NAME")
	listOnly := os.Getenv("LIST_OPEN_PRS") == "true"
	prNumberStr := os.Getenv("PR_NUMBER")
	if listOnly {
		prNumberStr = "0"
	}
	configPath := os.Getenv("CONFIG_PATH")
	reproPassStr := os.Getenv("REPRO_PASS")
	reproMessage := os.Getenv("REPRO_MESSAGE")

	if repoOwner == "" || repoName == "" || prNumberStr == "" {
		return fmt.Errorf("missing required environment variables")
	}

	prNumber, err := strconv.Atoi(prNumberStr)
	if err != nil || (!listOnly && prNumber <= 0) {
		return fmt.Errorf("invalid PR_NUMBER")
	}

	// Load configuration
	config, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Fetch PR information
	ctx := context.Background()
	client := &GitHubClient{
		token:   token,
		owner:   repoOwner,
		repo:    repoName,
		apiBase: "https://api.github.com",
		client:  &http.Client{Timeout: 30 * time.Second},
		config:  config,
	}

	if listOnly {
		prs, err := client.GetOpenPRs(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(duplicateSnapshot{Repository: repoOwner + "/" + repoName, CollectedAt: time.Now().UTC(), PRs: prs})
	}

	pr, err := client.GetPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("failed to fetch PR: %w", err)
	}

	// Fetch open PRs for duplicate detection
	openPRs, err := client.duplicates(ctx, prNumber, os.Getenv("DUPLICATE_SNAPSHOT_PATH"))
	if err != nil {
		return fmt.Errorf("failed to fetch open PRs: %w", err)
	}

	// Re-fetch all relevant evidence after duplicate discovery. A changing
	// snapshot must never become a candidate, even if both snapshots pass.
	fresh, err := client.GetPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("failed to recheck PR: %w", err)
	}
	if !sameEvidence(pr, fresh) {
		fresh.SnapshotConsistent = false
		fresh.SnapshotMessage = "metadata, files, checks, reviews, threads or receipts changed between reads"
	}
	pr = fresh
	if expected := os.Getenv("EXPECTED_HEAD_SHA"); expected != "" && expected != pr.HeadSHA {
		pr.SnapshotConsistent = false
		pr.SnapshotMessage = "head differs from workflow snapshot"
	}
	if expected := os.Getenv("EXPECTED_BASE_SHA"); expected != "" && expected != pr.BaseSHA {
		pr.SnapshotConsistent = false
		pr.SnapshotMessage = "base differs from workflow snapshot"
	}

	// Parse repro result
	var reproResult *greenlane.ReproResult
	reproInconclusiveStr := os.Getenv("REPRO_INCONCLUSIVE")
	reproTestIDsStr := os.Getenv("REPRO_TEST_IDS")

	if reproPassStr != "" || reproInconclusiveStr != "" {
		reproPass := reproPassStr == "true"
		reproInconclusive := reproInconclusiveStr == "true"

		var testIDs []string
		if reproTestIDsStr != "" {
			testIDs = strings.Split(reproTestIDsStr, ",")
		}

		reproResult = &greenlane.ReproResult{
			HeadSHA:      os.Getenv("REPRO_HEAD_SHA"),
			BaseSHA:      os.Getenv("REPRO_BASE_SHA"),
			Pass:         reproPass,
			Inconclusive: reproInconclusive,
			Message:      reproMessage,
			TestIDs:      testIDs,
		}
	}

	// Run evaluation
	evaluator := greenlane.NewEvaluator(config)
	result := evaluator.Evaluate(pr, reproResult, openPRs)

	// Output results
	jsonOut, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}

	fmt.Println(string(jsonOut))

	// Write summary for GitHub Actions
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		if err := writeSummary(summaryPath, result); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write summary: %v\n", err)
		}
	}

	// Set conclusion for check run
	if result.Verdict == greenlane.VerdictWouldMerge {
		fmt.Fprintln(os.Stderr, "::notice::Would merge if live")
	} else {
		fmt.Fprintf(os.Stderr, "::warning::Would not merge: %s\n", result.Summary)
	}

	return nil
}

func loadConfig(path string) (greenlane.Config, error) {
	if path == "" {
		return greenlane.DefaultConfig(), nil
	}

	// Optional JSON override, unset in the shadow workflow. Fields it omits
	// keep their defaults.
	data, err := os.ReadFile(path)
	if err != nil {
		return greenlane.Config{}, err
	}

	config := greenlane.DefaultConfig()
	if err := json.Unmarshal(data, &config); err != nil {
		return greenlane.Config{}, err
	}

	return config, nil
}

func writeSummary(path string, result greenlane.EvaluationResult) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write summary in markdown
	fmt.Fprintf(f, "# Green Lane Shadow Evaluation\n\n")
	fmt.Fprintf(f, "**Verdict:** %s\n\n", result.Verdict)
	fmt.Fprintf(f, "**Lane:** %s\n\n", result.Lane)
	fmt.Fprintf(f, "**PR:** #%d\n", result.PRNumber)
	fmt.Fprintf(f, "**Head SHA:** `%s`\n", result.HeadSHA)
	fmt.Fprintf(f, "**Evaluated at:** %s\n\n", result.EvaluatedAt.Format("2006-01-02 15:04:05 UTC"))

	fmt.Fprintf(f, "**Advisory candidate:** %t (never merge authorization)\n\n%s\n\n", result.AdvisoryCandidate, result.CandidateMessage)
	fmt.Fprintf(f, "## Rule Results\n\n")
	for _, rule := range result.Rules {
		status := "✅ PASS"
		if !rule.Pass {
			if rule.Inconclusive {
				status = "⚠️ INCONCLUSIVE"
			} else {
				status = "❌ FAIL"
			}
		}
		fmt.Fprintf(f, "### %s %s\n", status, rule.Name)
		fmt.Fprintf(f, "%s\n\n", rule.Message)
	}

	fmt.Fprintf(f, "## Summary\n\n")
	fmt.Fprintf(f, "%s\n\n", result.Summary)

	if result.Verdict == greenlane.VerdictWouldMerge {
		fmt.Fprintf(f, "---\n")
		fmt.Fprintf(f, "✨ **This PR would be merged automatically if green-lane were live.**\n\n")
		fmt.Fprintf(f, "Shadow mode: no action taken. The real gate App, cross-vendor reviewer Apps, ")
		fmt.Fprintf(f, "and provenance infrastructure do not exist yet.\n")
	} else {
		fmt.Fprintf(f, "---\n")
		fmt.Fprintf(f, "⏸️ **This PR would NOT be merged and would wait for manual review.**\n")
	}

	return nil
}
