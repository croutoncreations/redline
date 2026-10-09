package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

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
	prNumberStr := os.Getenv("PR_NUMBER")
	configPath := os.Getenv("CONFIG_PATH")
	reproPassStr := os.Getenv("REPRO_PASS")
	reproMessage := os.Getenv("REPRO_MESSAGE")

	if repoOwner == "" || repoName == "" || prNumberStr == "" {
		return fmt.Errorf("missing required environment variables")
	}

	prNumber, err := strconv.Atoi(prNumberStr)
	if err != nil {
		return fmt.Errorf("invalid PR_NUMBER: %w", err)
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
		client:  &http.Client{},
	}

	pr, err := client.GetPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("failed to fetch PR: %w", err)
	}

	// Fetch open PRs for duplicate detection
	openPRs, err := client.GetOpenPRs(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch open PRs: %w", err)
	}

	// Parse repro result
	var reproResult *greenlane.ReproResult
	if reproPassStr != "" {
		reproPass := reproPassStr == "true"
		reproResult = &greenlane.ReproResult{
			Pass:    reproPass,
			Message: reproMessage,
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
		fmt.Println("::notice::Would merge if live")
	} else {
		fmt.Printf("::warning::Would not merge: %s\n", result.Summary)
	}

	return nil
}

func loadConfig(path string) (greenlane.Config, error) {
	if path == "" {
		// Return default config
		return greenlane.Config{
			MaxChangedLines:    150,
			MaxLockfileChanges: 300,
			MaxFiles:           6,
			RequiredChecks: []string{
				"go",
				"dashboard",
				"linux-smoke",
				"windows-smoke",
				"macos",
				"macos-intel",
			},
			DeniedPaths: getDefaultDeniedPaths(),
		}, nil
	}

	// Load from file (YAML parsing would go here)
	// For simplicity, using JSON for now
	data, err := os.ReadFile(path)
	if err != nil {
		return greenlane.Config{}, err
	}

	var config greenlane.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return greenlane.Config{}, err
	}

	return config, nil
}

func getDefaultDeniedPaths() []string {
	return []string{
		// Common denied paths
		".github/workflows/**",
		".github/actions/**",
		"**/.env*",
		"**/*secret*",
		"**/*credential*",
		"**/*.pem",
		"**/*.p12",
		"**/*.keystore",
		"**/*.jks",
		"CODEOWNERS",

		// Green-lane controls
		".github/green-lane.yml",
		".github/green-lane/**",
		".github/prompts/**",

		// Test runner config
		"playwright.config.*",

		// Redline-specific
		"internal/apiauth/**",
		"internal/nativeusage/credentials*",
		"macos/Sources/RedlineKit/APICredentialStore.swift",
		".goreleaser.yaml",
		".github/release-notes-*",
		"scripts/package-macos-release.sh",
		"scripts/generate-sparkle-appcast.sh",
		"scripts/rehearse-macos-release-vm.sh",
		"scripts/lib/macos-signing.sh",
		"**/*.entitlements",
		"macos/**/Info.plist",
		"internal/store/sqlite.go",
	}
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
	fmt.Fprintf(f, "**PR:** #%d\n", result.PRNumber)
	fmt.Fprintf(f, "**Head SHA:** `%s`\n", result.HeadSHA)
	fmt.Fprintf(f, "**Evaluated at:** %s\n\n", result.EvaluatedAt.Format("2006-01-02 15:04:05 UTC"))

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

// GitHubClient wraps GitHub API calls
type GitHubClient struct {
	token   string
	owner   string
	repo    string
	apiBase string
	client  *http.Client
}

func (c *GitHubClient) GetPR(ctx context.Context, number int) (greenlane.PRInfo, error) {
	// Fetch PR details
	prURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, c.owner, c.repo, number)
	prData, err := c.doRequest(ctx, "GET", prURL)
	if err != nil {
		return greenlane.PRInfo{}, err
	}

	var prResp struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Draft  bool   `json:"draft"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"base"`
		Mergeable *bool `json:"mergeable"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}

	if err := json.Unmarshal(prData, &prResp); err != nil {
		return greenlane.PRInfo{}, err
	}

	// Fetch files
	filesURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files", c.apiBase, c.owner, c.repo, number)
	filesData, err := c.doRequest(ctx, "GET", filesURL)
	if err != nil {
		return greenlane.PRInfo{}, err
	}

	var filesResp []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Changes   int    `json:"changes"`
	}

	if err := json.Unmarshal(filesData, &filesResp); err != nil {
		return greenlane.PRInfo{}, err
	}

	files := make([]greenlane.PRFile, len(filesResp))
	for i, f := range filesResp {
		files[i] = greenlane.PRFile{
			Filename:  f.Filename,
			Status:    f.Status,
			Additions: f.Additions,
			Deletions: f.Deletions,
			Changes:   f.Changes,
		}
	}

	// Fetch check runs
	checksURL := fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs",
		c.apiBase, c.owner, c.repo, prResp.Head.SHA)
	checksData, err := c.doRequest(ctx, "GET", checksURL)
	if err != nil {
		return greenlane.PRInfo{}, err
	}

	var checksResp struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			App        struct {
				ID int64 `json:"id"`
			} `json:"app"`
		} `json:"check_runs"`
	}

	if err := json.Unmarshal(checksData, &checksResp); err != nil {
		return greenlane.PRInfo{}, err
	}

	checkRuns := make([]greenlane.CheckRun, len(checksResp.CheckRuns))
	for i, c := range checksResp.CheckRuns {
		checkRuns[i] = greenlane.CheckRun{
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			AppID:      c.App.ID,
		}
	}

	// Fetch reviews
	reviewsURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", c.apiBase, c.owner, c.repo, number)
	reviewsData, err := c.doRequest(ctx, "GET", reviewsURL)
	if err != nil {
		return greenlane.PRInfo{}, err
	}

	var reviewsResp []struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State       string `json:"state"`
		CommitID    string `json:"commit_id"`
		SubmittedAt string `json:"submitted_at"`
	}

	if err := json.Unmarshal(reviewsData, &reviewsResp); err != nil {
		return greenlane.PRInfo{}, err
	}

	reviews := make([]greenlane.Review, len(reviewsResp))
	for i, r := range reviewsResp {
		reviews[i] = greenlane.Review{
			ID:       r.ID,
			User:     r.User.Login,
			State:    r.State,
			CommitID: r.CommitID,
		}
	}

	mergeable := false
	if prResp.Mergeable != nil {
		mergeable = *prResp.Mergeable
	}

	return greenlane.PRInfo{
		Number:     prResp.Number,
		Title:      prResp.Title,
		Body:       prResp.Body,
		HeadSHA:    prResp.Head.SHA,
		BaseSHA:    prResp.Base.SHA,
		BaseBranch: prResp.Base.Ref,
		Draft:      prResp.Draft,
		Mergeable:  mergeable,
		Author:     prResp.User.Login,
		Files:      files,
		CheckRuns:  checkRuns,
		Reviews:    reviews,
	}, nil
}

func (c *GitHubClient) GetOpenPRs(ctx context.Context) ([]greenlane.PRSummary, error) {
	// Fetch all open PRs (paginated)
	var allPRs []greenlane.PRSummary
	page := 1
	perPage := 100

	for {
		url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=%d&page=%d",
			c.apiBase, c.owner, c.repo, perPage, page)
		data, err := c.doRequest(ctx, "GET", url)
		if err != nil {
			return nil, err
		}

		var prs []struct {
			Number int    `json:"number"`
			Body   string `json:"body"`
		}

		if err := json.Unmarshal(data, &prs); err != nil {
			return nil, err
		}

		if len(prs) == 0 {
			break
		}

		for _, pr := range prs {
			// Extract root cause from body
			rootCause := ""
			lines := strings.Split(pr.Body, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "Root-Cause:") {
					rootCause = strings.TrimSpace(strings.TrimPrefix(line, "Root-Cause:"))
					break
				}
			}

			allPRs = append(allPRs, greenlane.PRSummary{
				Number:    pr.Number,
				RootCause: rootCause,
				// Note: Not fetching files for each PR to keep this fast
				// In a real implementation, you'd need to fetch files for accurate duplicate detection
			})
		}

		if len(prs) < perPage {
			break
		}
		page++
	}

	return allPRs, nil
}

func (c *GitHubClient) doRequest(ctx context.Context, method, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
