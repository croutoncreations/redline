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
	// workflow_run always executes the default-branch workflow file at the main
	// tip; it is only a wake-up signal. No pull_request* event may run this.
	if len(triggers) != 3 {
		t.Fatalf("live workflow must only support schedule, dispatch and CI completion: %v", triggers)
	}
	for _, name := range []string{"schedule", "workflow_dispatch", "workflow_run"} {
		if _, ok := triggers[name]; !ok {
			t.Fatalf("missing trigger %s", name)
		}
	}
	var on struct {
		WorkflowRun struct {
			Workflows []string `yaml:"workflows"`
			Types     []string `yaml:"types"`
		} `yaml:"workflow_run"`
	}
	if err := wf.On.Decode(&on); err != nil {
		t.Fatal(err)
	}
	run := on.WorkflowRun
	if len(run.Workflows) != 1 || run.Workflows[0] != "CI" || len(run.Types) != 1 || run.Types[0] != "completed" {
		t.Fatalf("workflow_run must only follow completed CI: %+v", run)
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
	for _, guard := range []string{"github.repository == 'croutoncreations/redline'", "github.ref == 'refs/heads/main'", "vars.GREEN_LANE_LIVE_ENABLED == 'true'",
		"github.event_name != 'workflow_run' ||", "github.event.workflow_run.event == 'pull_request'",
		"github.event.workflow_run.conclusion == 'success'", "github.event.workflow_run.head_repository.full_name == github.repository"} {
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
			if step.With["retries"] != "0" || step.With["script"] != "const auditToken = process.env.GREEN_LANE_ACCESS_AUDIT_TOKEN;\ndelete process.env.GREEN_LANE_ACCESS_AUDIT_TOKEN;\nconst accessAudit = auditToken ? new github.constructor({auth:auditToken,request:{retries:0}}) : undefined;\nawait require('./.github/green-lane/live.cjs')({github,accessAudit,context,core});\n" {
				t.Fatal("unexpected merge entry point or retry configuration")
			}
			if step.Env["GREEN_LANE_ACCESS_AUDIT_TOKEN"] != "${{ secrets.GREEN_LANE_ACCESS_AUDIT_TOKEN }}" {
				t.Fatal("missing scoped access-audit secret")
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
