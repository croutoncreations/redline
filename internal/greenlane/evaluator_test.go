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
