package ciworkflows

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The merge-queue gate judges the queued PR, so it must be built from the
// merge group's base (trusted main), never from the merge group, which
// contains the PR's own code.
func TestQueueGateRunsTrustedCode(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Needs           []string          `yaml:"needs"`
			If              string            `yaml:"if"`
			ContinueOnError any               `yaml:"continue-on-error"`
			Permissions     map[string]string `yaml:"permissions"`
			Steps           []struct {
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				COE  any               `yaml:"continue-on-error"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["green-lane-queue"]
	if !ok {
		t.Fatal("ci.yml has no green-lane-queue job")
	}
	// It runs after every other required job and is never skipped: a
	// skipped required check counts as passing.
	required := []string{"actionlint", "go", "homebrew", "dashboard", "linux-smoke", "windows-smoke", "macos", "macos-intel"}
	for _, r := range required {
		if !slices.Contains(job.Needs, r) {
			t.Errorf("green-lane-queue does not wait for %s", r)
		}
	}
	if job.If != "${{ always() }}" {
		t.Errorf("green-lane-queue job if = %q, want ${{ always() }}", job.If)
	}
	if job.ContinueOnError != nil {
		t.Error("green-lane-queue must not continue on error")
	}
	for scope, level := range job.Permissions {
		if level != "read" {
			t.Errorf("green-lane-queue has %s: %s; it must be read-only", scope, level)
		}
	}
	checkouts, gate, build := 0, false, false
	for _, s := range job.Steps {
		if s.COE != nil {
			t.Errorf("gate step %q must not continue on error", s.Run+s.Uses)
		}
		if strings.Contains(s.Run, "go build") {
			build = s.Run == `go build -o "$RUNNER_TEMP/green-lane-check" ./cmd/green-lane-check` && s.If == "github.event_name == 'merge_group'"
		}
		if s.If == "github.event_name != 'merge_group'" && !strings.Contains(s.Run, "refs/heads/gh-readonly-queue/*) ") {
			t.Error("non-merge-group fallback must fail on queue branches")
		}
		if strings.HasPrefix(s.Uses, "actions/checkout@") {
			checkouts++
			if s.With["ref"] != "${{ github.event.merge_group.base_sha }}" {
				t.Errorf("gate checks out %q, want the merge group's base_sha", s.With["ref"])
			}
			if s.With["persist-credentials"] != "false" {
				t.Error("gate checkout must not persist credentials")
			}
		}
		if s.Env["QUEUE_GATE"] == "true" {
			gate = true
			if s.If != "github.event_name == 'merge_group'" {
				t.Errorf("gate step runs on %q, want merge_group only", s.If)
			}
		}
		if s.Uses != "" && !strings.HasPrefix(s.Uses, "actions/checkout@") && !strings.HasPrefix(s.Uses, "actions/setup-go@") {
			t.Errorf("unexpected action %s in the gate job", s.Uses)
		}
		if strings.Contains(s.Run, "merge_group.head") || strings.Contains(s.Run, "git checkout") {
			t.Errorf("gate step may run merge-group code: %q", s.Run)
		}
	}
	if checkouts != 1 || !gate || !build {
		t.Fatalf("want one base checkout, the gate build and the gate step, got %d checkouts, gate=%v build=%v", checkouts, gate, build)
	}
}
