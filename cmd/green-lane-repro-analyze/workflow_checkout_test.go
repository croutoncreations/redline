package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const greenLaneWorkflow = "../../.github/workflows/green-lane-shadow.yml"

type workflowStep struct {
	Name             string            `yaml:"name"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	With             map[string]string `yaml:"with"`
}

type workflowDoc struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		If          string            `yaml:"if"`
		Permissions map[string]string `yaml:"permissions"`
		Steps       []workflowStep    `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadGreenLaneWorkflow(t *testing.T) workflowDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(greenLaneWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	var doc workflowDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("workflow is not valid YAML: %v", err)
	}
	return doc
}

// evalExpr evaluates the small subset of GitHub Actions expressions the
// checkout refs use: property lookups, 'string' literals, ==, && and ||
// (left to right, with && binding tighter than ||), returning "" for
// unknown properties as Actions does.
func evalExpr(expr string, ctx map[string]string) string {
	expr = strings.TrimSpace(expr)
	if inner, ok := strings.CutPrefix(expr, "${{"); ok {
		expr = strings.TrimSpace(strings.TrimSuffix(inner, "}}"))
	}
	value := func(term string) string {
		term = strings.TrimSpace(term)
		if strings.HasPrefix(term, "'") && strings.HasSuffix(term, "'") {
			return strings.Trim(term, "'")
		}
		return ctx[term]
	}
	for _, alternative := range strings.Split(expr, "||") {
		result := ""
		truthy := true
		for _, conjunct := range strings.Split(alternative, "&&") {
			if left, right, ok := strings.Cut(conjunct, "=="); ok {
				if value(left) == value(right) {
					result = "true"
				} else {
					result, truthy = "", false
				}
			} else {
				result = value(conjunct)
				truthy = result != ""
			}
			if !truthy {
				break
			}
		}
		if truthy {
			return result
		}
	}
	return ""
}

// gitFixture builds a remote whose PR branch was merged into main and then
// deleted, which is the state GitHub is in when a workflow_run triggered by
// the PR's CI run starts after the merge.
func gitFixture(t *testing.T) (remote, mainSHA, prSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote = filepath.Join(dir, "remote.git")
	run(dir, "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(dir, "work")
	run(dir, "clone", "-q", remote, work)
	run(work, "commit", "-q", "--allow-empty", "-m", "root")
	run(work, "push", "-q", "origin", "HEAD:main")
	run(work, "checkout", "-q", "-b", "setup-pr")
	run(work, "commit", "-q", "--allow-empty", "-m", "pr")
	prSHA = run(work, "rev-parse", "HEAD")
	run(work, "push", "-q", "origin", "setup-pr")
	run(work, "checkout", "-q", "main")
	run(work, "merge", "-q", "--no-ff", "-m", "merge", "setup-pr")
	mainSHA = run(work, "rev-parse", "HEAD")
	run(work, "push", "-q", "origin", "main")
	run(work, "push", "-q", "origin", "--delete", "setup-pr")
	return remote, mainSHA, prSHA
}

// checkoutFetches reports whether actions/checkout's fetch of ref (a branch
// name or a commit SHA) succeeds against remote.
func checkoutFetches(t *testing.T, remote, ref string) bool {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	refspec := "+" + ref + ":refs/remotes/origin/checkout"
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(ref) {
		refspec = "+refs/heads/" + ref + ":refs/remotes/origin/" + ref
	}
	cmd := exec.Command("git", "-c", "protocol.version=2", "fetch", "--no-tags", "--depth=1", "origin", refspec)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	return cmd.Run() == nil
}

// Run 38174350504: a workflow_run for the PR's CI run started after the PR
// was merged and its branch deleted. The evaluate job checked out
// workflow_run.head_branch by name, every fetch failed, and the job failed.
// Every checkout in the workflow must name an exact commit that still exists
// after the branch is gone.
func TestGreenLaneCheckoutsSurviveADeletedPRBranch(t *testing.T) {
	remote, mainSHA, prSHA := gitFixture(t)
	events := map[string]map[string]string{
		"workflow_run after merge and branch deletion": {
			"github.event_name":                     "workflow_run",
			"github.sha":                            mainSHA, // default-branch commit the workflow runs at
			"github.event.workflow_run.event":       "pull_request",
			"github.event.workflow_run.head_branch": "setup-pr",
			"github.event.workflow_run.head_sha":    prSHA,
			"steps.pr_info.outputs.head_sha":        prSHA,
			"steps.pr_info.outputs.base_sha":        mainSHA,
			"needs.repro.outputs.head_sha":          prSHA,
		},
		"pull_request_review": {
			"github.event_name":                  "pull_request_review",
			"github.sha":                         prSHA, // refs/pull/N/merge: contains PR code
			"github.event.pull_request.base.ref": "main",
			"github.event.pull_request.base.sha": mainSHA,
			"github.event.pull_request.head.ref": "setup-pr",
			"github.event.pull_request.head.sha": prSHA,
			"steps.pr_info.outputs.head_sha":     prSHA,
			"steps.pr_info.outputs.base_sha":     mainSHA,
			"needs.repro.outputs.head_sha":       prSHA,
		},
		"pull_request": {
			"github.event_name":                  "pull_request",
			"github.sha":                         prSHA, // refs/pull/N/merge: contains PR code
			"github.event.pull_request.base.ref": "main",
			"github.event.pull_request.base.sha": mainSHA,
			"github.event.pull_request.head.ref": "setup-pr",
			"github.event.pull_request.head.sha": prSHA,
			"steps.pr_info.outputs.head_sha":     prSHA,
			"steps.pr_info.outputs.base_sha":     mainSHA,
			"needs.repro.outputs.head_sha":       prSHA,
		},
	}
	checkouts := 0
	for jobName, job := range loadGreenLaneWorkflow(t).Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/checkout@") {
				continue
			}
			checkouts++
			expr, ok := step.With["ref"]
			if !ok {
				t.Errorf("jobs.%s checkout has no ref; it must pin an exact commit", jobName)
				continue
			}
			for event, ctx := range events {
				ref := evalExpr(expr, ctx)
				if ref == "" {
					t.Errorf("jobs.%s checkout ref %q is empty for %s", jobName, expr, event)
					continue
				}
				if ref != mainSHA && ref != prSHA {
					t.Errorf("jobs.%s checkout ref %q resolves to %q for %s; want an exact commit SHA", jobName, expr, ref, event)
				}
				// evaluate builds and runs the evaluator with checks: write,
				// and every "trusted" checkout supplies the repro tooling, so
				// they must come from the trusted default/base commit, never
				// from the PR head.
				trusted := jobName == "evaluate" || step.With["path"] == "trusted"
				if trusted && ref != mainSHA {
					t.Errorf("jobs.%s trusted checkout resolves to %q for %s; want the trusted base/default commit %s", jobName, ref, event, mainSHA)
				}
				if !checkoutFetches(t, remote, ref) {
					t.Errorf("jobs.%s checkout of %q for %s fails once the PR branch is deleted", jobName, ref, event)
				}
			}
		}
	}
	if checkouts == 0 {
		t.Fatal("expected checkout steps in the green-lane workflow; found none")
	}
}

// When an earlier step is skipped or fails, ${{ steps.X.outputs.Y }} is "".
// Pasted into an actions/github-script body that leaves invalid JavaScript
// ("check_run_id: ,"), so the if: always() step meant to report the failure
// crashes instead (run 38174350504). Values must reach scripts through env,
// so no body may contain an expression, and every body must parse.
func TestGreenLaneScriptsStayValidWhenOutputsAreMissing(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}
	scripts := 0
	for jobName, job := range loadGreenLaneWorkflow(t).Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/github-script@") {
				continue
			}
			scripts++
			script := step.With["script"]
			if strings.Contains(script, "${{") {
				t.Errorf("jobs.%s step %q pastes a ${{ }} expression into its script; pass it via env and read process.env", jobName, step.Name)
			}
			// What the script becomes when every expression renders empty;
			// github-script wraps the body in an async function.
			empty := regexp.MustCompile(`\$\{\{.*?\}\}`).ReplaceAllString(script, "")
			body := "(async function () {\n" + empty + "\n})"
			file := filepath.Join(t.TempDir(), "script.js")
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(node, "--check", file).CombinedOutput(); err != nil {
				t.Errorf("jobs.%s step %q is not valid JavaScript when its ${{ }} values are empty:\n%s", jobName, step.Name, out)
			}
		}
	}
	if scripts == 0 {
		t.Fatal("expected github-script steps in the green-lane workflow; found none")
	}
}

// A PR must not be able to change the code that scores it. Every go build in
// the workflow must run inside the trusted (base-branch) checkout, the
// trusted binary must be built before PR code is checked out, and nothing
// may "go run" from the PR checkout (the old analyze step ran
// "go run ./cmd/green-lane-repro-analyze" in the PR's own workspace).
func TestGreenLaneToolingComesFromTrustedCode(t *testing.T) {
	builds := 0
	for jobName, job := range loadGreenLaneWorkflow(t).Jobs {
		trustedBuilt := false
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "go run") {
				t.Errorf("jobs.%s step %q uses go run; build from the trusted checkout instead", jobName, step.Name)
			}
			if strings.Contains(step.Run, "go build") {
				builds++
				if step.WorkingDirectory != "trusted" && jobName != "evaluate" {
					t.Errorf("jobs.%s step %q builds outside the trusted checkout", jobName, step.Name)
				}
				trustedBuilt = true
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["path"] == "pr" && !trustedBuilt {
				t.Errorf("jobs.%s checks out the PR before building trusted tooling", jobName)
			}
		}
	}
	if builds == 0 {
		t.Fatal("expected go build steps in the green-lane workflow")
	}
	// The repro job runs PR code in the same run and cache scope, and code
	// on a hosted runner can save a cache entry under setup-go's predictable
	// key. Any job that builds trusted tooling must not restore that cache.
	for jobName, job := range loadGreenLaneWorkflow(t).Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/setup-go@") && step.With["cache"] != "false" {
				t.Errorf("jobs.%s setup-go has cache %q; want \"false\" so PR code cannot poison trusted builds", jobName, step.With["cache"])
			}
			if strings.HasPrefix(step.Uses, "actions/cache@") {
				t.Errorf("jobs.%s uses actions/cache; trusted builds must not restore caches PR code can write", jobName)
			}
		}
	}
	jobs := loadGreenLaneWorkflow(t).Jobs
	for _, name := range []string{"repro", "repro-analyze"} {
		found := false
		for _, step := range jobs[name].Steps {
			if strings.Contains(step.Run, "green-lane-repro\" plan") {
				found = true
			}
		}
		if !found {
			t.Errorf("jobs.%s does not compute the repro plan with the trusted binary", name)
		}
	}
}

// A workflow_run runs in the default branch's context and cache scope, which
// release.yml restores with secrets present. The repro job runs PR code, so
// on workflow_run it must be limited to PRs from this repository.
func TestGreenLaneReproSkipsForkCodeOnWorkflowRun(t *testing.T) {
	cond := loadGreenLaneWorkflow(t).Jobs["repro"].If
	if !strings.Contains(cond, "github.event.workflow_run.head_repository.full_name == github.repository") {
		t.Errorf("jobs.repro if: %q must restrict workflow_run to same-repository heads", cond)
	}
}

// Shadow mode: jobs that touch PR code are read-only, the only write scope
// anywhere is checks: write on evaluate (to post the report-only check), and
// nothing in the workflow can merge, approve or enable auto-merge.
func TestGreenLaneStaysShadowOnly(t *testing.T) {
	doc := loadGreenLaneWorkflow(t)
	for scope, level := range doc.Permissions {
		if level == "write" && scope != "checks" {
			t.Errorf("workflow-level permission %s: write", scope)
		}
	}
	for jobName, job := range doc.Jobs {
		if job.Permissions == nil {
			t.Errorf("jobs.%s has no explicit permissions block", jobName)
		}
		for scope, level := range job.Permissions {
			if level == "write" && (jobName != "evaluate" || scope != "checks") {
				t.Errorf("jobs.%s has %s: write", jobName, scope)
			}
		}
	}
	raw, err := os.ReadFile(filepath.FromSlash(greenLaneWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"pulls.merge", "merge_method", "enablePullRequestAutoMerge", "gh pr merge", "createReview", "event: 'APPROVE'", "addLabels"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("workflow contains %q; green lane is shadow-only", forbidden)
		}
	}
}
