package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWorkflowPrefixesPackageDirsWithDotSlash locks in the fix for the false
// "Compile failed on base commit" verdict: test_dirs must be ./relative.
func TestWorkflowPrefixesPackageDirsWithDotSlash(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(greenLaneWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `echo "./$dir"`) && !strings.Contains(text, `echo "./"`) {
		t.Fatal("workflow must prefix package dirs with ./ before passing them to go test")
	}
	// Guard against regressing to bare dirname output as the only path form.
	if !strings.Contains(text, "Package dirs must be ./relative") {
		t.Fatal("workflow should document why ./relative package dirs are required")
	}
}

// TestWorkflowOverlaysTestdataOnBase ensures changed testdata/ under test
// packages is copied onto the base commit along with *_test.go files.
func TestWorkflowOverlaysTestdataOnBase(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(greenLaneWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "overlay_files.txt") {
		t.Fatal("workflow must build an overlay_files list")
	}
	if !strings.Contains(text, "/testdata") {
		t.Fatal("workflow must include package testdata/ in the base overlay")
	}
	if !strings.Contains(text, "done < overlay_files.txt") {
		t.Fatal("base overlay must copy from overlay_files.txt, not only changed_tests.txt")
	}
}

// TestWorkflowRunsAnalyzerFromTrustedBase ensures scoring code comes from the
// PR base commit, not the PR head checkout.
func TestWorkflowRunsAnalyzerFromTrustedBase(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(greenLaneWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "/tmp/trusted-analyzer") {
		t.Fatal("analyzer must run from a trusted-analyzer checkout")
	}
	if !regexp.MustCompile(`go run \./cmd/green-lane-repro-analyze`).MatchString(text) {
		t.Fatal("expected go run of green-lane-repro-analyze")
	}
	// The analyze step must not `go run` from $GITHUB_WORKSPACE (PR head).
	analyzeIdx := strings.Index(text, "Analyze repro results")
	if analyzeIdx < 0 {
		t.Fatal("missing Analyze repro results step")
	}
	snippet := text[analyzeIdx:]
	if end := strings.Index(snippet, "\n      - name:"); end > 0 {
		snippet = snippet[:end]
	}
	if strings.Contains(snippet, `cd "$GITHUB_WORKSPACE"`) && strings.Contains(snippet, "go run ./cmd/green-lane-repro-analyze") {
		// Allow copying artifacts back to workspace, but go run must be from trusted dir.
		if !strings.Contains(snippet, "cd /tmp/trusted-analyzer") {
			t.Fatal("analyze step still runs analyzer from GITHUB_WORKSPACE (PR code)")
		}
	}
	if !strings.Contains(snippet, "cd /tmp/trusted-analyzer") {
		t.Fatal("analyze step must cd to trusted-analyzer before go run")
	}
}
