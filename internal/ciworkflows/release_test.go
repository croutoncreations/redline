package ciworkflows

import (
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestReleasePublishWaitsForGates pins the release gate: the job that
// publishes (and is the only one allowed to write) must need both the CI gate
// and the Homebrew lint.
func TestReleasePublishWaitsForGates(t *testing.T) {
	wf := load(t, "release.yml")
	type job struct {
		Needs       []string
		Permissions map[string]string
	}
	jobs := map[string]job{}
	for id, node := range wf.Jobs {
		var raw struct {
			Needs       yaml.Node         `yaml:"needs"`
			Permissions map[string]string `yaml:"permissions"`
		}
		if err := node.Decode(&raw); err != nil {
			t.Fatalf("release.yml job %s: %v", id, err)
		}
		// needs: is either a single job ID or a list of them.
		jobs[id] = job{Needs: triggerNames(raw.Needs), Permissions: raw.Permissions}
	}
	for _, id := range []string{"ci-gate", "homebrew-check", "goreleaser"} {
		if _, ok := jobs[id]; !ok {
			t.Fatalf("release.yml has no %q job; jobs = %v", id, jobIDs(wf))
		}
	}
	for _, gate := range []string{"ci-gate", "homebrew-check"} {
		if !slices.Contains(jobs["goreleaser"].Needs, gate) {
			t.Errorf("goreleaser needs = %v, missing %q", jobs["goreleaser"].Needs, gate)
		}
	}
	for id, j := range jobs {
		if j.Permissions["contents"] == "write" && id != "goreleaser" {
			t.Errorf("job %s has contents: write; only goreleaser should", id)
		}
	}
}

// Execute the event selector extracted from the real workflow. PR-event CI
// must never satisfy the release gate, while explicit main CI dispatch must.
func TestReleaseCIGateAcceptsOnlyMainPushOrDispatch(t *testing.T) {
	wf := load(t, "release.yml")
	var job struct {
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	}
	node := wf.Jobs["ci-gate"]
	if err := node.Decode(&job); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "gh run list") {
			script = step.Run
		}
	}
	if !strings.Contains(script, `--commit "${GITHUB_SHA}" --branch main`) {
		t.Fatal("release gate lost exact commit/main filter")
	}
	match := regexp.MustCompile(`latest="\$\(jq -c '([^']+)'`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatal("release selector not found")
	}
	for _, tc := range []struct{ input, want string }{
		{`[{"databaseId":1,"event":"push"},{"databaseId":2,"event":"pull_request"}]`, `{"databaseId":1,"event":"push"}`},
		{`[{"databaseId":1,"event":"push"},{"databaseId":2,"event":"workflow_dispatch"}]`, `{"databaseId":2,"event":"workflow_dispatch"}`},
		{`[{"databaseId":3,"event":"pull_request_target"}]`, ""},
	} {
		cmd := exec.Command("jq", "-c", match[1])
		cmd.Stdin = strings.NewReader(tc.input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("jq: %v: %s", err, out)
		}
		if strings.TrimSpace(string(out)) != tc.want {
			t.Fatalf("got %s want %s", out, tc.want)
		}
	}
}
