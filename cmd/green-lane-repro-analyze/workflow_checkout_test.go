package main

import (
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These workflows publish reports from trusted code and never execute a PR.
func TestShadowWorkflowsTrustBoundary(t *testing.T) {
	for _, name := range []string{"green-lane-shadow.yml", "green-lane-reconcile.yml"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../.github/workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Permissions map[string]string `yaml:"permissions"`
				Jobs        map[string]struct {
					If          string            `yaml:"if"`
					Permissions map[string]string `yaml:"permissions"`
					Steps       []struct {
						Uses string            `yaml:"uses"`
						Run  string            `yaml:"run"`
						With map[string]string `yaml:"with"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Permissions["contents"] != "read" || doc.Permissions["pull-requests"] != "read" {
				t.Fatal("shadow must not have merge or approval permissions")
			}
			for jobName, job := range doc.Jobs {
				if jobName == "evaluate" && !strings.Contains(job.If, "default_branch") {
					t.Fatal("dispatch must reject non-default refs")
				}
				for permission, value := range job.Permissions {
					if value == "write" && permission != "checks" {
						t.Fatalf("unexpected write permission: %s", permission)
					}
				}
				for _, step := range job.Steps {
					if strings.HasPrefix(step.Uses, "actions/checkout@") {
						ref := step.With["ref"]
						if step.With["persist-credentials"] != "false" || !trustedShadowCheckout(ref) {
							t.Fatalf("unsafe checkout: %+v", step.With)
						}
						if jobName == "invalidate-closed" {
							t.Fatal("close invalidation must not check out code")
						}
					}
					if strings.Contains(step.Run, "green-lane-repro") {
						t.Fatal("shadow must not execute PR repro code")
					}
					if script := step.With["script"]; script != "" {
						if strings.Contains(script, "${{") {
							t.Fatal("inline expression in JavaScript")
						}
						if node, err := exec.LookPath("node"); err == nil {
							cmd := exec.Command(node, "--check")
							cmd.Stdin = strings.NewReader("(async()=>{\n" + script + "\n})")
							if out, err := cmd.CombinedOutput(); err != nil {
								t.Fatalf("invalid script: %s: %v", out, err)
							}
						}
					}
				}
			}
		})
	}
}

// Allow only the two explicit trusted expressions. A substring check for "head"
// rejects refs/heads/... while failing to describe the actual trust boundary.
func trustedShadowCheckout(ref string) bool {
	return ref == "${{ github.sha }}" || ref == "${{ github.event_name == 'pull_request_review' && format('refs/heads/{0}', github.event.repository.default_branch) || github.sha }}"
}

func TestShadowCheckoutRejectsPRCode(t *testing.T) {
	for _, ref := range []string{"", "main", "${{ github.event.pull_request.head.sha }}", "${{ github.ref }}", "refs/pull/195/merge", "${{ github.event.pull_request.base.sha }}", "${{ github.sha || github.event.pull_request.head.sha }}"} {
		if trustedShadowCheckout(ref) {
			t.Fatalf("untrusted checkout accepted: %s", ref)
		}
	}
}
