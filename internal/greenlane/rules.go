package greenlane

import (
	"fmt"
	"path/filepath"
	"strings"
)

// checkSize implements Rule 1: size ≤ 150 changed lines
func (e *Evaluator) checkSize(pr PRInfo) RuleResult {
	lockfilePatterns := []string{
		"**/go.sum",
		"**/package-lock.json",
		"**/pnpm-lock.yaml",
		"**/yarn.lock",
		"*.lock",
		"**/Package.resolved",
		"**/gradle.lockfile",
	}

	generatedPatterns := []string{
		"**/*.pb.go",
		"**/*_gen.go",
		"**/*.generated.*",
		"dist/**",
	}

	regularLines := 0
	lockfileLines := 0
	fileCount := 0
	var lockfileChanges []string
	var generatedChanges []string

	for _, file := range pr.Files {
		fileCount++
		changes := file.Additions + file.Deletions

		if matchesAnyPattern(file.Filename, lockfilePatterns) {
			lockfileLines += changes
			lockfileChanges = append(lockfileChanges, file.Filename)
		} else if matchesAnyPattern(file.Filename, generatedPatterns) {
			lockfileLines += changes
			generatedChanges = append(generatedChanges, file.Filename)
		} else {
			regularLines += changes
		}
	}

	// Check file count
	if fileCount > e.config.MaxFiles {
		return RuleResult{
			Name:    "Size",
			Pass:    false,
			Message: fmt.Sprintf("Too many files: %d (max %d)", fileCount, e.config.MaxFiles),
		}
	}

	// Check regular lines
	if regularLines > e.config.MaxChangedLines {
		return RuleResult{
			Name:    "Size",
			Pass:    false,
			Message: fmt.Sprintf("Too many changed lines: %d (max %d)", regularLines, e.config.MaxChangedLines),
		}
	}

	// Check lockfile/generated lines
	if lockfileLines > e.config.MaxLockfileChanges {
		return RuleResult{
			Name:    "Size",
			Pass:    false,
			Message: fmt.Sprintf("Too many lockfile/generated changes: %d (max %d)", lockfileLines, e.config.MaxLockfileChanges),
		}
	}

	// Check for orphaned lockfiles/generated files
	// Map each lockfile to its required manifest
	lockfileManifests := map[string]string{
		"go.sum":            "go.mod",
		"package-lock.json": "package.json",
		"pnpm-lock.yaml":    "package.json",
		"yarn.lock":         "package.json",
		"Package.resolved":  "Package.swift",
		"gradle.lockfile":   "build.gradle",
	}

	generatedSources := map[string][]string{
		".pb.go":      {".proto"},
		"_gen.go":     {".go"},
		".generated.": {".go", ".ts", ".js"},
	}

	// Check lockfiles
	for _, lockfile := range lockfileChanges {
		manifest, ok := lockfileManifests[filepath.Base(lockfile)]
		if !ok {
			// Generic *.lock pattern - skip validation
			continue
		}

		// Check if the matching manifest in the same directory exists in the PR
		lockDir := filepath.Dir(lockfile)
		manifestPath := filepath.Join(lockDir, manifest)
		if lockDir == "." {
			manifestPath = manifest
		}

		hasManifest := false
		for _, file := range pr.Files {
			if file.Filename == manifestPath {
				hasManifest = true
				break
			}
		}

		if !hasManifest {
			return RuleResult{
				Name:    "Size",
				Pass:    false,
				Message: fmt.Sprintf("Lockfile %s changed without matching %s in same directory", lockfile, manifest),
			}
		}
	}

	// Check generated files
	for _, genFile := range generatedChanges {
		hasSource := false

		// Find which pattern matched
		var requiredExts []string
		for pattern, exts := range generatedSources {
			if strings.Contains(genFile, pattern) {
				requiredExts = exts
				break
			}
		}

		if len(requiredExts) == 0 {
			// dist/** or unknown pattern - skip validation
			continue
		}

		// Check if any source file with required extension exists
		genDir := filepath.Dir(genFile)
		for _, file := range pr.Files {
			if filepath.Dir(file.Filename) != genDir {
				continue
			}
			for _, ext := range requiredExts {
				if strings.HasSuffix(file.Filename, ext) && !matchesAnyPattern(file.Filename, generatedPatterns) {
					hasSource = true
					break
				}
			}
			if hasSource {
				break
			}
		}

		if !hasSource {
			return RuleResult{
				Name:    "Size",
				Pass:    false,
				Message: fmt.Sprintf("Generated file %s changed without source change in same directory", genFile),
			}
		}
	}

	return RuleResult{
		Name:    "Size",
		Pass:    true,
		Message: fmt.Sprintf("OK: %d changed lines in %d files (%d lockfile/generated)", regularLines, fileCount, lockfileLines),
	}
}

// checkRepro implements Rule 2: test fails before, passes after
func (e *Evaluator) checkRepro(reproResult *ReproResult) RuleResult {
	if reproResult == nil {
		return RuleResult{
			Name:         "Test Repro",
			Pass:         false,
			Inconclusive: true,
			Message:      "No repro job result available",
		}
	}

	if reproResult.Inconclusive {
		return RuleResult{
			Name:         "Test Repro",
			Pass:         false,
			Inconclusive: true,
			Message:      reproResult.Message,
		}
	}

	if !reproResult.Pass {
		return RuleResult{
			Name:    "Test Repro",
			Pass:    false,
			Message: reproResult.Message,
		}
	}

	return RuleResult{
		Name:    "Test Repro",
		Pass:    true,
		Message: fmt.Sprintf("OK: tests %v failed on base, passed on head", reproResult.TestIDs),
	}
}

// checkCI implements Rule 3: CI status
func (e *Evaluator) checkCI(pr PRInfo) RuleResult {
	pending := []string{}
	failed := []string{}
	missing := []string{}

	// Map of required checks we've seen
	seen := make(map[string]bool)

	for _, check := range pr.CheckRuns {
		for _, required := range e.config.RequiredChecks {
			if check.Name == required {
				seen[required] = true

				if check.Status != "completed" {
					pending = append(pending, check.Name)
				} else if check.Conclusion != "success" {
					failed = append(failed, fmt.Sprintf("%s (%s)", check.Name, check.Conclusion))
				}
			}
		}
	}

	// Check for missing required checks
	for _, required := range e.config.RequiredChecks {
		if !seen[required] {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		return RuleResult{
			Name:    "CI",
			Pass:    false,
			Message: fmt.Sprintf("Missing required checks: %s", strings.Join(missing, ", ")),
		}
	}

	if len(pending) > 0 {
		return RuleResult{
			Name:    "CI",
			Pass:    false,
			Message: fmt.Sprintf("Checks pending: %s", strings.Join(pending, ", ")),
		}
	}

	if len(failed) > 0 {
		return RuleResult{
			Name:    "CI",
			Pass:    false,
			Message: fmt.Sprintf("Checks failed: %s", strings.Join(failed, ", ")),
		}
	}

	return RuleResult{
		Name:    "CI",
		Pass:    true,
		Message: fmt.Sprintf("OK: all %d required checks passed", len(e.config.RequiredChecks)),
	}
}

// checkReviewer implements Rule 4: cross-vendor reviewer
func (e *Evaluator) checkReviewer(pr PRInfo) RuleResult {
	// Check for CodeRabbit review first
	codeRabbitReview := checkCodeRabbitReview(pr)

	// In shadow mode, we don't have the full provenance infrastructure yet
	// Check for any approved review on the head SHA
	hasApproval := false
	hasRequestChanges := false

	for _, review := range pr.Reviews {
		if review.CommitID != pr.HeadSHA {
			continue
		}

		// Skip CodeRabbit reviews since we handle them separately
		if review.User == "coderabbitai[bot]" {
			continue
		}

		if review.State == "APPROVED" {
			hasApproval = true
		}
		if review.State == "CHANGES_REQUESTED" {
			hasRequestChanges = true
		}
	}

	// Build message with CodeRabbit status
	var message strings.Builder
	if codeRabbitReview.Reviewed {
		if codeRabbitReview.Approved {
			message.WriteString("CodeRabbit: approved with ")
		} else if codeRabbitReview.RequestedChanges {
			message.WriteString("CodeRabbit: requested changes with ")
		} else {
			message.WriteString("CodeRabbit: reviewed with ")
		}
		message.WriteString(fmt.Sprintf("%d findings. ", codeRabbitReview.FindingsCount))
	} else {
		message.WriteString("CodeRabbit: no real review on head SHA. ")
	}

	if !hasApproval {
		message.WriteString("No human approval on current head SHA. ")
	} else {
		message.WriteString("Human approval found, but ")
	}

	message.WriteString("Cross-vendor provenance not verified (shadow mode: needs real Apps)")

	// Either CodeRabbit or human requesting changes should fail
	if codeRabbitReview.RequestedChanges || hasRequestChanges {
		return RuleResult{
			Name:    "Reviewer",
			Pass:    false,
			Message: message.String(),
		}
	}

	// In shadow mode, we note that provenance checking is not yet implemented
	return RuleResult{
		Name:    "Reviewer",
		Pass:    false,
		Message: message.String(),
	}
}

// CodeRabbitReview represents CodeRabbit's review status
type CodeRabbitReview struct {
	Reviewed         bool
	Approved         bool
	RequestedChanges bool
	FindingsCount    int
}

// checkCodeRabbitReview determines if CodeRabbit provided a qualifying review
func checkCodeRabbitReview(pr PRInfo) CodeRabbitReview {
	result := CodeRabbitReview{}

	// Look for reviews from coderabbitai[bot]
	for _, review := range pr.Reviews {
		if review.User != "coderabbitai[bot]" {
			continue
		}

		// Must be on the current head SHA
		if review.CommitID != pr.HeadSHA {
			continue
		}

		// Check for explicit approve or request changes
		if review.State == "APPROVED" {
			result.Reviewed = true
			result.Approved = true
			return result
		}

		if review.State == "CHANGES_REQUESTED" {
			result.Reviewed = true
			result.RequestedChanges = true
			// Count findings from review comments
			result.FindingsCount = countReviewComments(pr, review.ID)
			return result
		}

		// For COMMENTED state, check if there are actual review comments (findings)
		if review.State == "COMMENTED" {
			findings := countReviewComments(pr, review.ID)
			if findings > 0 {
				result.Reviewed = true
				result.FindingsCount = findings
				return result
			}
		}
	}

	return result
}

// countReviewComments counts inline review comments for a review
func countReviewComments(pr PRInfo, reviewID int64) int {
	count := 0
	for _, comment := range pr.ReviewComments {
		if comment.ReviewID == reviewID {
			count++
		}
	}
	return count
}

// checkDuplicates implements Rule 5: no duplicate PRs
func (e *Evaluator) checkDuplicates(pr PRInfo, openPRs []PRSummary) RuleResult {
	rootCause := extractRootCause(pr.Body)

	if rootCause == "" {
		return RuleResult{
			Name:    "Duplicates",
			Pass:    false,
			Message: "Missing Root-Cause: trailer in PR body",
		}
	}

	// Get non-test files from this PR
	var thisFiles []string
	for _, f := range pr.Files {
		if !isTestFile(f.Filename) {
			thisFiles = append(thisFiles, f.Filename)
		}
	}

	// Check for exact root cause match
	for _, other := range openPRs {
		if other.Number == pr.Number {
			continue
		}

		if normalizeRootCause(other.RootCause) == normalizeRootCause(rootCause) {
			return RuleResult{
				Name:    "Duplicates",
				Pass:    false,
				Message: fmt.Sprintf("Duplicate of PR #%d (same Root-Cause: %s)", other.Number, rootCause),
			}
		}

		// Check for overlapping files with similar root cause slugs
		if tokenOverlap(rootCause, other.RootCause) >= 0.5 {
			for _, otherFile := range other.Files {
				for _, thisFile := range thisFiles {
					if otherFile == thisFile {
						return RuleResult{
							Name:    "Duplicates",
							Pass:    false,
							Message: fmt.Sprintf("Likely duplicate of PR #%d (overlapping file %s, similar root cause)", other.Number, thisFile),
						}
					}
				}
			}
		}
	}

	return RuleResult{
		Name:    "Duplicates",
		Pass:    true,
		Message: fmt.Sprintf("OK: no duplicate found for Root-Cause: %s", rootCause),
	}
}

// checkDeniedPaths implements Rule 6: denied paths
func (e *Evaluator) checkDeniedPaths(pr PRInfo) RuleResult {
	var denied []string

	for _, file := range pr.Files {
		for _, pattern := range e.config.DeniedPaths {
			if matchPattern(file.Filename, pattern) {
				denied = append(denied, file.Filename)
				break
			}
		}
	}

	if len(denied) > 0 {
		return RuleResult{
			Name:    "Denied Paths",
			Pass:    false,
			Message: fmt.Sprintf("Touches denied paths: %s", strings.Join(denied, ", ")),
		}
	}

	return RuleResult{
		Name:    "Denied Paths",
		Pass:    true,
		Message: "OK: no denied paths touched",
	}
}

// Helper functions

func matchesAnyPattern(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchPattern(path, pattern) {
			return true
		}
	}
	return false
}

func matchPattern(path, pattern string) bool {
	// Handle ** for directory recursion
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			prefix := parts[0]
			suffix := parts[1]

			// If suffix starts with /, it's a path separator
			if strings.HasPrefix(suffix, "/") {
				suffix = suffix[1:]
			}

			// Check if path starts with prefix (if any) and contains/ends with suffix
			if prefix != "" && !strings.HasPrefix(path, prefix) {
				return false
			}

			// For suffix with wildcards, check each path component
			if strings.Contains(suffix, "*") {
				// Get the part after the prefix
				remaining := path
				if prefix != "" {
					remaining = strings.TrimPrefix(path, prefix)
				}

				// Check if any part of the path matches the suffix pattern
				pathParts := strings.Split(remaining, "/")
				for _, part := range pathParts {
					matched, _ := filepath.Match(suffix, part)
					if matched {
						return true
					}
				}
				// Also check the full remaining path
				matched, _ := filepath.Match(suffix, remaining)
				return matched
			}

			// For literal suffix, check if path contains it
			if suffix == "" {
				return strings.HasPrefix(path, prefix)
			}
			return strings.HasPrefix(path, prefix) && strings.Contains(path, suffix)
		}
	}

	// Handle * wildcards
	if strings.Contains(pattern, "*") {
		matched, _ := filepath.Match(pattern, filepath.Base(path))
		return matched
	}

	// Exact match or prefix match for directories
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}

	return path == pattern || strings.HasPrefix(path, pattern+"/")
}

func extractRootCause(body string) string {
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Root-Cause:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Root-Cause:"))
		}
	}
	return ""
}

func normalizeRootCause(rc string) string {
	return strings.ToLower(strings.TrimSpace(rc))
}

func tokenOverlap(a, b string) float64 {
	tokensA := strings.FieldsFunc(strings.ToLower(a), func(r rune) bool {
		return r == '/' || r == '#' || r == '-' || r == '_'
	})
	tokensB := strings.FieldsFunc(strings.ToLower(b), func(r rune) bool {
		return r == '/' || r == '#' || r == '-' || r == '_'
	})

	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0
	}

	common := 0
	for _, ta := range tokensA {
		for _, tb := range tokensB {
			if ta == tb {
				common++
				break
			}
		}
	}

	total := len(tokensA)
	if len(tokensB) > total {
		total = len(tokensB)
	}

	return float64(common) / float64(total)
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.Contains(path, "/testdata/") ||
		strings.Contains(path, "/__tests__/") ||
		strings.Contains(path, ".test.") ||
		strings.Contains(path, ".spec.")
}
