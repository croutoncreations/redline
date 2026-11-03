package ciworkflows

import "testing"

// Review-triggered evaluation must use current trusted tooling even when the
// PR base predates it. Other events retain their immutable event revision.
func TestGreenLaneTrustedCheckout(t *testing.T) {
	wf := load(t, "green-lane-shadow.yml")
	job := wf.Jobs["evaluate"]
	var config struct {
		Steps []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	}
	if err := job.Decode(&config); err != nil {
		t.Fatal(err)
	}
	var ref string
	for _, step := range config.Steps {
		if step.Uses == "actions/checkout@v4" {
			ref = step.With["ref"]
			if step.With["persist-credentials"] != "false" {
				t.Fatal("checkout must not persist credentials")
			}
		}
	}
	if ref == "" {
		t.Fatal("missing explicit trusted checkout ref")
	}
	for _, branch := range []string{"main", "trunk"} {
		for _, event := range []string{"pull_request_review", "pull_request_target", "workflow_run", "workflow_dispatch"} {
			t.Run(branch+"/"+event, func(t *testing.T) {
				ctx := map[string]any{
					"github.event_name":                      event,
					"github.event.repository.default_branch": branch,
					"github.sha":                             "trusted-event-sha",
					"github.event.pull_request.base.sha":     "old-base-without-publisher",
					"github.event.pull_request.head.sha":     "untrusted-pr-head",
					"github.ref":                             "refs/pull/185/merge",
				}
				want := "trusted-event-sha"
				if event == "pull_request_review" {
					want = "refs/heads/" + branch
				}
				if got := interpolate(t, ref, ctx); got != want {
					t.Fatalf("checkout=%q want %q", got, want)
				}
			})
		}
	}
}
