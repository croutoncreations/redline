package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const shadowWorkflow = "../../.github/workflows/green-lane-shadow.yml"

type workflowFile struct {
	Jobs map[string]struct {
		Needs   any               `yaml:"needs"`
		Outputs map[string]string `yaml:"outputs"`
		Steps   []struct {
			ID string `yaml:"id"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

var needsOutputRef = regexp.MustCompile(`needs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)`)
var stepOutputRef = regexp.MustCompile(`^\$\{\{\s*steps\.([A-Za-z0-9_-]+)\.outputs\.[A-Za-z0-9_-]+\s*\}\}$`)

// GitHub Actions silently resolves an undeclared needs.<job>.outputs.<name>
// to "", so a job that never exports its step outputs makes every consumer
// take its "nothing happened" branch. Every output the shadow workflow reads
// from another job must be declared on that job and mapped from a real step.
func TestShadowWorkflowExportsEveryJobOutputItReads(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(shadowWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	var workflow workflowFile
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("workflow is not valid YAML: %v", err)
	}

	refs := map[string]bool{}
	for _, match := range needsOutputRef.FindAllStringSubmatch(string(raw), -1) {
		refs[match[1]+"."+match[2]] = true
	}
	if len(refs) == 0 {
		t.Fatal("expected the evaluate job to read repro outputs; found no needs.*.outputs.* references")
	}
	keys := make([]string, 0, len(refs))
	for ref := range refs {
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	for _, ref := range keys {
		job, output, _ := strings.Cut(ref, ".")
		producer, ok := workflow.Jobs[job]
		if !ok {
			t.Errorf("needs.%s.outputs.%s: job %q does not exist", job, output, job)
			continue
		}
		value, ok := producer.Outputs[output]
		if !ok {
			t.Errorf("needs.%s.outputs.%s is read but job %q does not declare outputs.%s", job, output, job, output)
			continue
		}
		step := stepOutputRef.FindStringSubmatch(strings.TrimSpace(value))
		if step == nil {
			t.Errorf("jobs.%s.outputs.%s = %q, want a steps.<id>.outputs.<name> mapping", job, output, value)
			continue
		}
		found := false
		for _, s := range producer.Steps {
			found = found || s.ID == step[1]
		}
		if !found {
			t.Errorf("jobs.%s.outputs.%s maps from step %q, which job %q does not have", job, output, step[1], job)
		}
	}
}
