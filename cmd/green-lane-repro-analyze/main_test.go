package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseResults(t *testing.T) {
	stream := `{"Action":"start","Package":"example.com/pkg"}
{"Action":"run","Test":"TestFoo","Package":"example.com/pkg"}
{"Action":"output","Test":"TestFoo","Package":"example.com/pkg","Output":"=== RUN   TestFoo\n"}
{"Action":"run","Test":"TestFoo/sub","Package":"example.com/pkg"}
{"Action":"pass","Test":"TestFoo/sub","Package":"example.com/pkg"}
{"Action":"fail","Test":"TestFoo","Package":"example.com/pkg","Elapsed":0.01}
{"Action":"pass","Test":"TestBar","Package":"example.com/pkg"}
{"Action":"skip","Test":"TestSkipped","Package":"example.com/pkg"}
{"Action":"fail","Package":"example.com/pkg"}
`
	res, err := parseResults(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"TestFoo": "fail", "TestBar": "pass", "TestSkipped": "skip"}
	if !reflect.DeepEqual(res.tests, want) {
		t.Errorf("tests = %v, want %v (a passing subtest must not mask its failing parent)", res.tests, want)
	}
	if !strings.Contains(res.output["TestFoo"], "=== RUN   TestFoo") {
		t.Errorf("output not captured: %q", res.output["TestFoo"])
	}
	if res.buildFailed {
		t.Error("buildFailed set for a stream that compiled")
	}
}

// This is the stream go test produced in every shadow run: the package was
// passed as "internal/notification", which go resolves inside GOROOT.
func TestParseResultsDetectsSetupFailure(t *testing.T) {
	stream := `{"ImportPath":"internal/foo","Action":"build-output","Output":"package internal/foo is not in std\n"}
{"ImportPath":"internal/foo","Action":"build-fail"}
{"Action":"start","Package":"internal/foo"}
{"Action":"output","Package":"internal/foo","Output":"FAIL\tinternal/foo [setup failed]\n"}
{"Action":"fail","Package":"internal/foo","Elapsed":0,"FailedBuild":"internal/foo"}
`
	res, err := parseResults(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if !res.buildFailed || !strings.Contains(res.buildOutput, "not in std") {
		t.Errorf("buildFailed=%v buildOutput=%q", res.buildFailed, res.buildOutput)
	}
}

func TestParseResultsRejectsBadStreams(t *testing.T) {
	for name, stream := range map[string]string{
		"malformed":  "{\"Action\":\"run\",\"Test\":\"TestFoo\"}\nnot json\n",
		"incomplete": "{\"Action\":\"run\",\"Test\":\"TestFoo\"}\n{\"Action\":\"pass\",\"Test\":\"TestFoo\"}\n",
	} {
		if _, err := parseResults(strings.NewReader(stream)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGoPackageArgIsRelative(t *testing.T) {
	for dir, want := range map[string]string{"internal/foo": "./internal/foo", ".": ".", "cmd/x/y": "./cmd/x/y"} {
		if got := goPackageArg(dir); got != want {
			t.Errorf("goPackageArg(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestChangedTestsIgnoresUnchangedFunctions(t *testing.T) {
	base := "package p\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestEdited(t *testing.T) {}\n"
	head := "package p\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestEdited(t *testing.T) { t.Log(1) }\nfunc TestNew(t *testing.T) {}\nfunc helper(t *testing.T) {}\nfunc Testable(t *testing.T) {}\nfunc TestMain(m *testing.M) {}\n"
	got, err := changedTests("p_test.go", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestEdited", "TestNew"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changedTests = %v, want %v", got, want)
	}
}

func TestWriteOutputsCannotInjectLines(t *testing.T) {
	var buf bytes.Buffer
	writeOutputs(&buf, Outcome{Message: "boom\npass=true"})
	if strings.Count(buf.String(), "\n") != 4 || strings.Contains(buf.String(), "\npass=true") {
		t.Errorf("message escaped onto its own line:\n%s", buf.String())
	}
}

// --- end-to-end fixtures --------------------------------------------------

type fixtureRepo struct {
	t    *testing.T
	dir  string
	base string
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	r := &fixtureRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	// The buggy code lives in internal/calc so the package path has the
	// internal/... shape that broke every shadow run.
	r.write("go.mod", "module example.com/fixture\n\ngo 1.21\n")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a - b }\n")
	r.write("internal/calc/calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestExisting(t *testing.T) {}\n")
	r.base = r.commit("base")
	return r
}

func (r *fixtureRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *fixtureRepo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fixtureRepo) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

// pipeline runs plan -> run -> analyze exactly as the workflow does.
func (r *fixtureRepo) pipeline(head string) (Plan, Outcome) {
	r.t.Helper()
	out := r.t.TempDir()
	planFile := filepath.Join(out, "plan.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan", "-repo", r.dir, "-base", r.base, "-head", head, "-out", planFile}, &stdout, &stderr); code != 0 {
		r.t.Fatalf("plan exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"run", "-repo", r.dir, "-plan", planFile, "-out", out}, &stdout, &stderr); code != 0 {
		r.t.Fatalf("run exit %d: %s", code, stderr.String())
	}
	plan, err := readPlan(planFile)
	if err != nil {
		r.t.Fatal(err)
	}
	return plan, analyze(plan, out)
}

const fixtureTest = `package calc

import (
	"os"
	"strings"
	"testing"
)

func TestAddFromFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.txt")
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "2+3=5" {
		t.Fatalf("unexpected fixture %q", raw)
	}
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`

// A PR with a genuine fails-before test, in an internal/... package, whose
// test reads a testdata fixture the PR adds, gets a real result on base:
// the test compiles, runs, and fails on the bug, then passes on head.
func TestReproGenuineFailsBeforeFixture(t *testing.T) {
	r := newFixtureRepo(t)
	r.git("checkout", "-q", "-b", "fix")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	r.write("internal/calc/add_test.go", fixtureTest)
	r.write("internal/calc/testdata/cases.txt", "2+3=5\n")
	head := r.commit("fix Add")

	plan, outcome := r.pipeline(head)
	if want := []string{"internal/calc.TestAddFromFixture"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v (unchanged TestExisting must be excluded)", plan.Tests(), want)
	}
	if !outcome.Pass || outcome.Inconclusive {
		t.Fatalf("outcome = %+v, want pass", outcome)
	}
	if !strings.Contains(outcome.BaseOutput, "Add(2, 3) = -1, want 5") {
		t.Errorf("base failed for the wrong reason; output:\n%s", outcome.BaseOutput)
	}
}

// A test-only PR adds a fixture-driven test for code that is already
// correct. Before testdata was overlaid, the fixture was missing on base,
// the test failed there with "fixture missing", and this looked like a repro.
func TestReproOverlaysTestdataOntoBase(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	r.base = r.commit("already correct")
	r.git("checkout", "-q", "-b", "tests-only")
	r.write("internal/calc/add_test.go", fixtureTest)
	r.write("internal/calc/testdata/cases.txt", "2+3=5\n")
	head := r.commit("add test")

	_, outcome := r.pipeline(head)
	if outcome.Pass || outcome.Inconclusive {
		t.Fatalf("outcome = %+v, want a conclusive fail", outcome)
	}
	if len(outcome.Tests) != 1 || outcome.Tests[0].Base != "pass" {
		t.Fatalf("base status = %+v, want pass (fixture must be present on base)", outcome.Tests)
	}
}

// A PR whose test calls a function that does not exist on base cannot show
// fails-before; that stays inconclusive, with go's message in the report.
func TestReproBaseCompileFailureIsInconclusive(t *testing.T) {
	r := newFixtureRepo(t)
	r.git("checkout", "-q", "-b", "new-api")
	r.write("internal/calc/sub.go", "package calc\n\nfunc Sub(a, b int) int { return a - b }\n")
	r.write("internal/calc/sub_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(3, 1) != 2 {\n\t\tt.Fatal()\n\t}\n}\n")
	head := r.commit("add Sub")

	_, outcome := r.pipeline(head)
	if !outcome.Inconclusive || !strings.Contains(outcome.Message, "Compile failed on base commit for ./internal/calc") ||
		!strings.Contains(outcome.Message, "undefined: Sub") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestReproNoChangedTests(t *testing.T) {
	r := newFixtureRepo(t)
	r.git("checkout", "-q", "-b", "docs")
	r.write("README.md", "hi\n")
	head := r.commit("docs")
	plan, outcome := r.pipeline(head)
	if len(plan.Packages) != 0 || !outcome.Inconclusive {
		t.Fatalf("plan=%+v outcome=%+v", plan, outcome)
	}
}
