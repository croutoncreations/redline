package ciworkflows

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const workflowDir = "../../.github/workflows"

type workflow struct {
	Name        string               `yaml:"name"`
	On          yaml.Node            `yaml:"on"`
	Concurrency *concurrency         `yaml:"concurrency"`
	Jobs        map[string]yaml.Node `yaml:"jobs"`
}

type concurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress string `yaml:"cancel-in-progress"`
}

func load(t *testing.T, file string) workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workflowDir, file))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("%s is not valid YAML: %v", file, err)
	}
	return wf
}

func triggerNames(n yaml.Node) []string {
	var names []string
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			names = append(names, n.Content[i].Value)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			names = append(names, c.Value)
		}
	case yaml.ScalarNode:
		names = append(names, n.Value)
	}
	sort.Strings(names)
	return names
}

func jobIDs(wf workflow) []string {
	ids := make([]string, 0, len(wf.Jobs))
	for id := range wf.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// event describes one triggering event as the expression contexts see it.
type event struct {
	name     string // github.event_name
	prNumber int    // github.event.pull_request.number, 0 = absent
	runID    int64  // github.run_id
}

func (e event) ctx(workflowName string) map[string]any {
	c := map[string]any{
		"github.workflow":   workflowName,
		"github.event_name": e.name,
		"github.run_id":     strconv.FormatInt(e.runID, 10),
	}
	if e.prNumber != 0 {
		c["github.event.pull_request.number"] = float64(e.prNumber)
	}
	return c
}

// resolve returns the group and cancel-in-progress values for an event.
func resolve(t *testing.T, wf workflow, e event) (string, bool) {
	t.Helper()
	ctx := e.ctx(wf.Name)
	group := interpolate(t, wf.Concurrency.Group, ctx)
	cancel := interpolate(t, wf.Concurrency.CancelInProgress, ctx)
	switch cancel {
	case "true":
		return group, true
	case "false":
		return group, false
	}
	t.Fatalf("cancel-in-progress resolved to %q, want true or false", cancel)
	return "", false
}

var inScope = []string{"ci.yml", "claude-code-review.yml"}

func TestSupersededPullRequestRunsAreCancelled(t *testing.T) {
	for _, file := range inScope {
		t.Run(file, func(t *testing.T) {
			wf := load(t, file)
			if wf.Concurrency == nil {
				t.Fatal("missing workflow-level concurrency")
			}
			first, cancel1 := resolve(t, wf, event{name: "pull_request", prNumber: 123, runID: 9001})
			second, cancel2 := resolve(t, wf, event{name: "pull_request", prNumber: 123, runID: 9002})
			if first != second {
				t.Fatalf("same PR update got different groups %q / %q", first, second)
			}
			if !cancel1 || !cancel2 {
				t.Fatal("pull_request runs must cancel the superseded run")
			}
			if want := wf.Name + "-pr-123"; first != want {
				t.Fatalf("group = %q, want %q", first, want)
			}
			other, _ := resolve(t, wf, event{name: "pull_request", prNumber: 124, runID: 9003})
			if other == first {
				t.Fatalf("different PRs share group %q", other)
			}
		})
	}
}

func TestNonPullRequestRunsAreUniqueAndNeverCancel(t *testing.T) {
	events := []event{
		{name: "push", runID: 123},              // push to main (CI)
		{name: "push", runID: 124},              // another main push
		{name: "push", runID: 125},              // tag push, same event name
		{name: "workflow_dispatch", runID: 126}, // manual
		{name: "schedule", runID: 127},
	}
	for _, file := range inScope {
		t.Run(file, func(t *testing.T) {
			wf := load(t, file)
			pr123, _ := resolve(t, wf, event{name: "pull_request", prNumber: 123, runID: 9001})
			seen := map[string]string{}
			for _, e := range events {
				group, cancel := resolve(t, wf, e)
				if cancel {
					t.Errorf("%s run %d must not cancel in-progress runs", e.name, e.runID)
				}
				if want := fmt.Sprintf("%s-run-%d", wf.Name, e.runID); group != want {
					t.Errorf("%s group = %q, want %q", e.name, group, want)
				}
				if group == pr123 {
					t.Errorf("run ID 123 shares a group with PR 123: %q", group)
				}
				if prev, ok := seen[group]; ok {
					t.Errorf("group %q shared by %s and %s", group, prev, e.name)
				}
				seen[group] = e.name
			}
		})
	}
}

func TestWorkflowsDoNotShareGroups(t *testing.T) {
	pr := event{name: "pull_request", prNumber: 123, runID: 9001}
	ci, _ := resolve(t, load(t, "ci.yml"), pr)
	review, _ := resolve(t, load(t, "claude-code-review.yml"), pr)
	if ci == review {
		t.Fatalf("CI and Claude review share group %q", ci)
	}
}

// TestTriggersAndJobsUnchanged pins the triggers and job IDs so the
// concurrency change cannot silently drop a required check or review trigger.
func TestTriggersAndJobsUnchanged(t *testing.T) {
	want := map[string]struct{ on, jobs []string }{
		"ci.yml": {
			on:   []string{"pull_request", "push"},
			jobs: []string{"actionlint", "dashboard", "go", "linux-smoke", "macos", "macos-intel", "windows-smoke"},
		},
		"claude-code-review.yml": {
			on:   []string{"pull_request"},
			jobs: []string{"claude-review"},
		},
	}
	for file, w := range want {
		wf := load(t, file)
		if got := triggerNames(wf.On); strings.Join(got, ",") != strings.Join(w.on, ",") {
			t.Errorf("%s triggers = %v, want %v", file, got, w.on)
		}
		if got := jobIDs(wf); strings.Join(got, ",") != strings.Join(w.jobs, ",") {
			t.Errorf("%s jobs = %v, want %v", file, got, w.jobs)
		}
	}
}

// TestOutOfScopeWorkflowsHaveNoConcurrency keeps Green Lane, the interactive
// @claude workflow and releases out of this cancellation scheme.
func TestOutOfScopeWorkflowsHaveNoConcurrency(t *testing.T) {
	for _, file := range []string{"green-lane-shadow.yml", "claude.yml", "release.yml"} {
		if wf := load(t, file); wf.Concurrency != nil {
			t.Errorf("%s unexpectedly has concurrency %+v", file, *wf.Concurrency)
		}
	}
}

func TestExpressionEvaluator(t *testing.T) {
	ctx := map[string]any{"a": "x", "n": float64(7), "empty": ""}
	cases := map[string]string{
		"a == 'x'":                              "true",
		"a == 'y'":                              "false",
		"a == 'x' && 'yes' || 'no'":             "yes",
		"a == 'y' && 'yes' || 'no'":             "no",
		"format('pr-{0}', n)":                   "pr-7",
		"missing && 'yes' || empty":             "",
		"(a == 'x') && format('{0}-{1}', a, n)": "x-7",
	}
	for expr, want := range cases {
		if got := toString(eval(t, expr, ctx)); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}
