package ciworkflows

import (
	"slices"
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
