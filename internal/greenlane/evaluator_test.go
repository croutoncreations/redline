package greenlane

import (
	"testing"
)

func TestCheckSize(t *testing.T) {
	config := Config{
		MaxChangedLines:    150,
		MaxLockfileChanges: 300,
		MaxFiles:           6,
	}
	eval := NewEvaluator(config)

	tests := []struct {
		name     string
		files    []PRFile
		wantPass bool
		wantMsg  string
	}{
		{
			name: "small PR passes",
			files: []PRFile{
				{Filename: "internal/foo/bar.go", Additions: 30, Deletions: 10},
				{Filename: "internal/foo/bar_test.go", Additions: 20, Deletions: 5},
			},
			wantPass: true,
		},
		{
			name: "too many lines fails",
			files: []PRFile{
				{Filename: "internal/foo/bar.go", Additions: 100, Deletions: 60},
			},
			wantPass: false,
			wantMsg:  "Too many changed lines",
		},
		{
			name: "lockfile doesn't count toward regular limit",
			files: []PRFile{
				{Filename: "internal/foo/bar.go", Additions: 50, Deletions: 50},
				{Filename: "go.mod", Additions: 5, Deletions: 2},
				{Filename: "go.sum", Additions: 200, Deletions: 100},
			},
			wantPass: true,
		},
		{
			name: "too many files fails",
			files: []PRFile{
				{Filename: "file1.go", Additions: 10, Deletions: 5},
				{Filename: "file2.go", Additions: 10, Deletions: 5},
				{Filename: "file3.go", Additions: 10, Deletions: 5},
				{Filename: "file4.go", Additions: 10, Deletions: 5},
				{Filename: "file5.go", Additions: 10, Deletions: 5},
				{Filename: "file6.go", Additions: 10, Deletions: 5},
				{Filename: "file7.go", Additions: 10, Deletions: 5},
			},
			wantPass: false,
			wantMsg:  "Too many files",
		},
		{
			name: "lockfile without manifest fails",
			files: []PRFile{
				{Filename: "go.sum", Additions: 50, Deletions: 20},
			},
			wantPass: false,
			wantMsg:  "Lockfile changed without manifest change",
		},
		{
			name: "lockfile with manifest passes",
			files: []PRFile{
				{Filename: "go.mod", Additions: 2, Deletions: 1},
				{Filename: "go.sum", Additions: 50, Deletions: 20},
			},
			wantPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{Files: tt.files}
			result := eval.checkSize(pr)

			if result.Pass != tt.wantPass {
				t.Errorf("checkSize() pass = %v, want %v", result.Pass, tt.wantPass)
			}

			if tt.wantMsg != "" && !contains(result.Message, tt.wantMsg) {
				t.Errorf("checkSize() message = %q, want to contain %q", result.Message, tt.wantMsg)
			}
		})
	}
}

func TestCheckDeniedPaths(t *testing.T) {
	config := Config{
		DeniedPaths: []string{
			".github/workflows/**",
			"internal/apiauth/**",
			"**/.env*",
			"**/*secret*",
		},
	}
	eval := NewEvaluator(config)

	tests := []struct {
		name     string
		files    []PRFile
		wantPass bool
		wantMsg  string
	}{
		{
			name: "regular files pass",
			files: []PRFile{
				{Filename: "internal/foo/bar.go"},
				{Filename: "cmd/redline/main.go"},
			},
			wantPass: true,
		},
		{
			name: "workflow file fails",
			files: []PRFile{
				{Filename: ".github/workflows/ci.yml"},
			},
			wantPass: false,
			wantMsg:  "Touches denied paths",
		},
		{
			name: "apiauth path fails",
			files: []PRFile{
				{Filename: "internal/apiauth/token.go"},
			},
			wantPass: false,
		},
		{
			name: "secret file fails",
			files: []PRFile{
				{Filename: "config/secrets.yaml"},
			},
			wantPass: false,
		},
		{
			name: "env file fails",
			files: []PRFile{
				{Filename: ".env.local"},
			},
			wantPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{Files: tt.files}
			result := eval.checkDeniedPaths(pr)

			if result.Pass != tt.wantPass {
				t.Errorf("checkDeniedPaths() pass = %v, want %v (message: %s)",
					result.Pass, tt.wantPass, result.Message)
			}
		})
	}
}

func TestCheckDuplicates(t *testing.T) {
	config := Config{}
	eval := NewEvaluator(config)

	tests := []struct {
		name     string
		prBody   string
		prFiles  []PRFile
		openPRs  []PRSummary
		wantPass bool
		wantMsg  string
	}{
		{
			name:     "no root cause fails",
			prBody:   "This is a PR without a root cause trailer",
			wantPass: false,
			wantMsg:  "Missing Root-Cause",
		},
		{
			name:    "unique root cause passes",
			prBody:  "Root-Cause: backend/routes/users#pagination-bug",
			prFiles: []PRFile{{Filename: "internal/routes/users.go"}},
			openPRs: []PRSummary{
				{Number: 123, RootCause: "backend/routes/posts#different-bug"},
			},
			wantPass: true,
		},
		{
			name:   "exact duplicate fails",
			prBody: "Root-Cause: backend/routes/users#pagination-bug",
			openPRs: []PRSummary{
				{Number: 123, RootCause: "backend/routes/users#pagination-bug"},
			},
			wantPass: false,
			wantMsg:  "Duplicate of PR #123",
		},
		{
			name:    "similar root cause with same file fails",
			prBody:  "Root-Cause: backend/routes/users#pagination",
			prFiles: []PRFile{{Filename: "internal/routes/users.go"}},
			openPRs: []PRSummary{
				{
					Number:    456,
					RootCause: "backend/routes/users#paginate",
					Files:     []string{"internal/routes/users.go"},
				},
			},
			wantPass: false,
			wantMsg:  "Likely duplicate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{
				Number: 999,
				Body:   tt.prBody,
				Files:  tt.prFiles,
			}
			result := eval.checkDuplicates(pr, tt.openPRs)

			if result.Pass != tt.wantPass {
				t.Errorf("checkDuplicates() pass = %v, want %v (message: %s)",
					result.Pass, tt.wantPass, result.Message)
			}

			if tt.wantMsg != "" && !contains(result.Message, tt.wantMsg) {
				t.Errorf("checkDuplicates() message = %q, want to contain %q",
					result.Message, tt.wantMsg)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{".github/workflows/ci.yml", ".github/workflows/**", true},
		{"internal/apiauth/token.go", "internal/apiauth/**", true},
		{"config/secrets.yaml", "**/*secret*", true},
		{".env.local", "**/.env*", true},
		{"internal/foo/bar.go", "internal/apiauth/**", false},
		{"go.sum", "*.lock", false},
		{"package.lock", "*.lock", true},
		{"dist/bundle.js", "dist/**", true},
	}

	for _, tt := range tests {
		t.Run(tt.path+" vs "+tt.pattern, func(t *testing.T) {
			got := matchPattern(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestExtractRootCause(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{
			body: "Root-Cause: backend/routes/users#pagination-bug",
			want: "backend/routes/users#pagination-bug",
		},
		{
			body: "Some text\nRoot-Cause: internal/store#query-timeout\nMore text",
			want: "internal/store#query-timeout",
		},
		{
			body: "Root-Cause:  spaces/around#value  ",
			want: "spaces/around#value",
		},
		{
			body: "No root cause here",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := extractRootCause(tt.body)
			if got != tt.want {
				t.Errorf("extractRootCause() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenOverlap(t *testing.T) {
	tests := []struct {
		a   string
		b   string
		min float64
	}{
		{"backend/routes/users#pagination", "backend/routes/users#paginate", 0.5},
		{"backend/routes/users#bug", "frontend/components/posts#bug", 0.2},
		{"internal/store#query", "internal/store#query", 1.0},
		{"foo/bar#baz", "completely/different#thing", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			got := tokenOverlap(tt.a, tt.b)
			if got < tt.min {
				t.Errorf("tokenOverlap(%q, %q) = %v, want >= %v", tt.a, tt.b, got, tt.min)
			}
		})
	}
}

func TestCheckReviewer(t *testing.T) {
	config := Config{}
	eval := NewEvaluator(config)

	tests := []struct {
		name           string
		reviews        []Review
		reviewComments []ReviewComment
		headSHA        string
		wantPass       bool
		wantMsg        string
	}{
		{
			name:     "no reviews fails",
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "CodeRabbit: no real review",
		},
		{
			name: "approved human review still needs provenance",
			reviews: []Review{
				{User: "human-reviewer", State: "APPROVED", CommitID: "abc123"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "Human approval found, but",
		},
		{
			name: "CodeRabbit with explicit approve counts",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "APPROVED", CommitID: "abc123"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "CodeRabbit: approved",
		},
		{
			name: "CodeRabbit with findings counts",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: "abc123"},
			},
			reviewComments: []ReviewComment{
				{ReviewID: 1, Path: "foo.go", Line: 10, Body: "Consider refactoring"},
				{ReviewID: 1, Path: "bar.go", Line: 20, Body: "Typo here"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "CodeRabbit: reviewed with 2 findings",
		},
		{
			name: "CodeRabbit comment without findings doesn't count",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: "abc123"},
			},
			reviewComments: []ReviewComment{},
			headSHA:        "abc123",
			wantPass:       false,
			wantMsg:        "CodeRabbit: no real review",
		},
		{
			name: "CodeRabbit on wrong SHA doesn't count",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "APPROVED", CommitID: "old-sha"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "CodeRabbit: no real review",
		},
		{
			name: "CodeRabbit requesting changes fails",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "CHANGES_REQUESTED", CommitID: "abc123"},
			},
			reviewComments: []ReviewComment{
				{ReviewID: 1, Path: "foo.go", Line: 10, Body: "Fix this"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "CodeRabbit: requested changes with 1 findings",
		},
		{
			name: "human requesting changes fails",
			reviews: []Review{
				{User: "human", State: "CHANGES_REQUESTED", CommitID: "abc123"},
			},
			headSHA:  "abc123",
			wantPass: false,
			wantMsg:  "No human approval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{
				HeadSHA:        tt.headSHA,
				Reviews:        tt.reviews,
				ReviewComments: tt.reviewComments,
			}
			result := eval.checkReviewer(pr)

			if result.Pass != tt.wantPass {
				t.Errorf("checkReviewer() pass = %v, want %v (message: %s)",
					result.Pass, tt.wantPass, result.Message)
			}

			if tt.wantMsg != "" && !contains(result.Message, tt.wantMsg) {
				t.Errorf("checkReviewer() message = %q, want to contain %q",
					result.Message, tt.wantMsg)
			}
		})
	}
}

func TestCheckRepro(t *testing.T) {
	config := Config{}
	eval := NewEvaluator(config)

	tests := []struct {
		name     string
		repro    *ReproResult
		wantPass bool
		wantMsg  string
	}{
		{
			name:     "no repro result is inconclusive",
			repro:    nil,
			wantPass: false,
			wantMsg:  "No repro job result available",
		},
		{
			name: "inconclusive repro",
			repro: &ReproResult{
				Inconclusive: true,
				Message:      "Compile failed on base",
			},
			wantPass: false,
			wantMsg:  "Compile failed on base",
		},
		{
			name: "passing repro",
			repro: &ReproResult{
				Pass:    true,
				TestIDs: []string{"TestFoo", "TestBar"},
				Message: "Tests failed on base, passed on head",
			},
			wantPass: true,
			wantMsg:  "failed on base, passed on head",
		},
		{
			name: "failing repro",
			repro: &ReproResult{
				Pass:    false,
				Message: "TestFoo did not fail on base",
			},
			wantPass: false,
			wantMsg:  "did not fail on base",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := eval.checkRepro(tt.repro)

			if result.Pass != tt.wantPass {
				t.Errorf("checkRepro() pass = %v, want %v (message: %s)",
					result.Pass, tt.wantPass, result.Message)
			}

			if tt.wantMsg != "" && !contains(result.Message, tt.wantMsg) {
				t.Errorf("checkRepro() message = %q, want to contain %q",
					result.Message, tt.wantMsg)
			}
		})
	}
}

func TestCodeRabbitReview(t *testing.T) {
	tests := []struct {
		name           string
		reviews        []Review
		reviewComments []ReviewComment
		headSHA        string
		wantReviewed   bool
		wantApproved   bool
		wantChanges    bool
		wantFindings   int
	}{
		{
			name:         "no CodeRabbit review",
			headSHA:      "abc123",
			wantReviewed: false,
		},
		{
			name: "CodeRabbit explicit approve",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "APPROVED", CommitID: "abc123"},
			},
			headSHA:      "abc123",
			wantReviewed: true,
			wantApproved: true,
		},
		{
			name: "CodeRabbit with findings",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: "abc123"},
			},
			reviewComments: []ReviewComment{
				{ReviewID: 1, Path: "foo.go", Line: 10},
				{ReviewID: 1, Path: "bar.go", Line: 20},
				{ReviewID: 1, Path: "baz.go", Line: 30},
			},
			headSHA:      "abc123",
			wantReviewed: true,
			wantFindings: 3,
		},
		{
			name: "CodeRabbit comment without findings",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "COMMENTED", CommitID: "abc123"},
			},
			headSHA:      "abc123",
			wantReviewed: false,
		},
		{
			name: "CodeRabbit on wrong SHA",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "APPROVED", CommitID: "old-sha"},
			},
			headSHA:      "abc123",
			wantReviewed: false,
		},
		{
			name: "CodeRabbit requesting changes",
			reviews: []Review{
				{ID: 1, User: "coderabbitai[bot]", State: "CHANGES_REQUESTED", CommitID: "abc123"},
			},
			reviewComments: []ReviewComment{
				{ReviewID: 1, Path: "foo.go", Line: 10},
			},
			headSHA:      "abc123",
			wantReviewed: true,
			wantChanges:  true,
			wantFindings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{
				HeadSHA:        tt.headSHA,
				Reviews:        tt.reviews,
				ReviewComments: tt.reviewComments,
			}
			result := checkCodeRabbitReview(pr)

			if result.Reviewed != tt.wantReviewed {
				t.Errorf("checkCodeRabbitReview() Reviewed = %v, want %v",
					result.Reviewed, tt.wantReviewed)
			}

			if result.Approved != tt.wantApproved {
				t.Errorf("checkCodeRabbitReview() Approved = %v, want %v",
					result.Approved, tt.wantApproved)
			}

			if result.RequestedChanges != tt.wantChanges {
				t.Errorf("checkCodeRabbitReview() RequestedChanges = %v, want %v",
					result.RequestedChanges, tt.wantChanges)
			}

			if result.FindingsCount != tt.wantFindings {
				t.Errorf("checkCodeRabbitReview() FindingsCount = %v, want %v",
					result.FindingsCount, tt.wantFindings)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && containsHelper(s, substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
