package greenlane

import (
	"strings"
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
				{Filename: "go.sum", Additions: 5, Deletions: 2},
			},
			wantPass: false,
			wantMsg:  "without matching go.mod",
		},
		{
			name: "lockfile with manifest passes",
			files: []PRFile{
				{Filename: "go.mod", Additions: 2, Deletions: 1},
				{Filename: "go.sum", Additions: 50, Deletions: 20},
			},
			wantPass: true,
		},
		{
			name: "lockfile with manifest in subdirectory passes",
			files: []PRFile{
				{Filename: "backend/go.mod", Additions: 2, Deletions: 1},
				{Filename: "backend/go.sum", Additions: 50, Deletions: 20},
			},
			wantPass: true,
		},
		{
			name: "lockfile without manifest in same directory fails",
			files: []PRFile{
				{Filename: "frontend/package-lock.json", Additions: 30, Deletions: 20},
				{Filename: "backend/package.json", Additions: 2, Deletions: 1},
			},
			wantPass: false,
			wantMsg:  "without matching package.json in same directory",
		},
		{
			name: "go.sum without go.mod in same directory fails",
			files: []PRFile{
				{Filename: "subdir/go.sum", Additions: 50, Deletions: 20},
				{Filename: "go.mod", Additions: 2, Deletions: 1},
			},
			wantPass: false,
			wantMsg:  "without matching go.mod in same directory",
		},
		{
			name: "generated proto file with source passes",
			files: []PRFile{
				{Filename: "api/types.proto", Additions: 10, Deletions: 5},
				{Filename: "api/types.pb.go", Additions: 200, Deletions: 100},
			},
			wantPass: true,
		},
		{
			name: "generated file without source in same directory fails",
			files: []PRFile{
				{Filename: "api/types.pb.go", Additions: 200, Deletions: 100},
			},
			wantPass: false,
			wantMsg:  "without source change in same directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := PRInfo{Files: tt.files}
			result := eval.checkSize(pr, eval.laneLimits(LaneCode))

			if result.Pass != tt.wantPass {
				t.Errorf("checkSize() pass = %v, want %v", result.Pass, tt.wantPass)
			}

			if tt.wantMsg != "" && !strings.Contains(result.Message, tt.wantMsg) {
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
		// Patterns with a directory part match the full path; these never
		// matched when * patterns were compared against the base name only.
		{".github/release-notes-draft.md", ".github/release-notes-*", true},
		{"internal/nativeusage/credentials_darwin.go", "internal/nativeusage/credentials*", true},
		{"docs/release-notes-draft.md", ".github/release-notes-*", false},
		{"web/playwright.config.ts", "playwright.config.*", true},
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
