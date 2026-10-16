package ciworkflows

import (
	"slices"
	"testing"
)

// TestReleasePublishWaitsForGates pins the release gate: the job that
// publishes (and is the only one allowed to write) must need both the CI gate
// and the Homebrew lint.
func TestReleasePublishWaitsForGates(t *testing.T) {
	wf := load(t, "release.yml")
	type job struct {
		Needs       []string          `yaml:"needs"`
		Permissions map[string]string `yaml:"permissions"`
	}
	jobs := map[string]job{}
	for id, node := range wf.Jobs {
		var j job
		if err := node.Decode(&j); err != nil {
			t.Fatalf("release.yml job %s (write needs as a list): %v", id, err)
		}
		jobs[id] = j
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
