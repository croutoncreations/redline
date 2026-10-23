package ciworkflows

import (
	"strings"
	"testing"
)

func TestGreenLaneLiveTrustBoundary(t *testing.T) {
	wf := load(t, "green-lane-live.yml")
	var triggers map[string]any
	if err := wf.On.Decode(&triggers); err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 2 {
		t.Fatalf("live workflow must only support schedule and dispatch: %v", triggers)
	}
	for _, name := range []string{"schedule", "workflow_dispatch"} {
		if _, ok := triggers[name]; !ok {
			t.Fatalf("missing trigger %s", name)
		}
	}
	if wf.Concurrency == nil || wf.Concurrency.Group != "green-lane-live-merge" || wf.Concurrency.CancelInProgress != "false" {
		t.Fatalf("live merges must be serialized without cancellation: %+v", wf.Concurrency)
	}
	if len(wf.Jobs) != 1 {
		t.Fatal("one merge worker only")
	}
	var job struct {
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			Env  map[string]string `yaml:"env"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	}
	node := wf.Jobs["merge"]
	if err := node.Decode(&job); err != nil {
		t.Fatal(err)
	}
	for _, guard := range []string{"github.repository == 'croutoncreations/redline'", "github.ref == 'refs/heads/main'", "vars.GREEN_LANE_LIVE_ENABLED == 'true'"} {
		if !strings.Contains(job.If, guard) {
			t.Fatalf("missing guard %s", guard)
		}
	}
	if len(job.Permissions) != 4 || job.Permissions["contents"] != "write" || job.Permissions["pull-requests"] != "write" || job.Permissions["checks"] != "read" || job.Permissions["actions"] != "write" {
		t.Fatalf("unexpected permissions: %v", job.Permissions)
	}
	checkout, runner := false, false
	for _, step := range job.Steps {
		if step.Uses == "actions/checkout@v4" {
			checkout = true
			if step.With["ref"] != "${{ github.sha }}" || step.With["persist-credentials"] != "false" {
				t.Fatal("must checkout trusted immutable event SHA without stored credentials")
			}
		}
		if step.Uses == "actions/github-script@v7" {
			runner = true
			if step.With["retries"] != "0" || step.With["script"] != "await require('./.github/green-lane/live.cjs')({github,context,core});\n" {
				t.Fatal("unexpected merge entry point or retry configuration")
			}
			if step.Env["LIVE_PILOT_ENABLED"] != "${{ vars.GREEN_LANE_LIVE_ENABLED }}" {
				t.Fatal("missing explicit opt-in")
			}
		}
	}
	if !checkout || !runner {
		t.Fatal("missing trusted checkout or live runner")
	}
}

func TestCIPostMergeDispatch(t *testing.T) {
	wf := load(t, "ci.yml")
	var triggers map[string]any
	if err := wf.On.Decode(&triggers); err != nil {
		t.Fatal(err)
	}
	if _, ok := triggers["workflow_dispatch"]; !ok {
		t.Fatal("token-authenticated merges require explicit CI dispatch support")
	}
}
