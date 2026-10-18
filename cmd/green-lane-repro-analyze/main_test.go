package main

import (
	"bytes"
	"fmt"
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
	base := "package p\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestEdited(t *testing.T) {}\nfunc TestMain(m *testing.M) {}\n"
	head := "package p\nimport \"testing\"\nfunc TestOld(t *testing.T) {}\nfunc TestEdited(t *testing.T) { t.Log(1) }\nfunc TestNew(t *testing.T) {}\nfunc helper(t *testing.T) {}\nfunc Testable(t *testing.T) {}\nfunc TestMain(m *testing.M) {}\n"
	got, err := changedTests("p_test.go", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestEdited", "TestNew"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changedTests = %v, want %v", got, want)
	}
}

// #183 review: a regression case added to a package-level table changes
// what TestX runs without changing TestX's text. Tests that use a changed
// top-level declaration, directly or through a helper, are scheduled.
func TestChangedTestsFollowsChangedPackageLevelInputs(t *testing.T) {
	base := `package p
import "testing"
var cases = []int{1, 2}
type checker struct{}
func (checker) ok(n int) bool { return n > 0 }
func run(t *testing.T) { for _, c := range cases { _ = c } }
func TestTable(t *testing.T) { for _, c := range cases { _ = c } }
func TestViaHelper(t *testing.T) { run(t) }
func TestMethod(t *testing.T) { _ = checker{}.ok(1) }
func TestUnrelated(t *testing.T) { cases := 1; _ = cases }
func TestOther(t *testing.T) {}
`
	head := strings.Replace(base, "{1, 2}", "{1, 2, 3}", 1)
	head = strings.Replace(head, "return n > 0", "return n >= 0", 1)
	got, err := changedTests("p_test.go", base, head)
	if err != nil {
		t.Fatal(err)
	}
	// TestUnrelated shadows the name locally; matching by identifier is
	// conservative, and an extra test only adds a fails-before requirement.
	want := []string{"TestMethod", "TestTable", "TestUnrelated", "TestViaHelper"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changedTests = %v, want %v (TestOther must be excluded)", got, want)
	}
}

// #183 review: dependencies are resolved across all of the package's test
// files, and import changes count as changes.
func TestChangedPackageTestsAcrossFilesAndImports(t *testing.T) {
	x := "package p\nimport \"testing\"\nfunc TestX(t *testing.T) { for range cases {} }\nfunc TestY(t *testing.T) {}\n"
	z := "package p\nimport (\n\t\"testing\"\n\tlib \"example.com/a\"\n)\nfunc TestZ(t *testing.T) { lib.Do() }\n"
	helpers := "package p\nvar cases = []int{1}\n"
	base := map[string]string{"p/x_test.go": x, "p/z_test.go": z, "p/helpers_test.go": helpers}
	cases := []struct {
		name string
		edit func(map[string]string)
		want []string
	}{
		{"table case in another file", func(m map[string]string) {
			m["p/helpers_test.go"] = "package p\nvar cases = []int{1, 2}\n"
		}, []string{"TestX"}},
		{"helper removed", func(m map[string]string) {
			m["p/helpers_test.go"] = "package p\n"
			m["p/x_test.go"] = x + "var cases = []int{}\n"
		}, []string{"TestX"}},
		{"aliased import path switched", func(m map[string]string) {
			m["p/z_test.go"] = strings.Replace(z, "example.com/a", "example.com/b", 1)
		}, []string{"TestZ"}},
		{"blank import added", func(m map[string]string) {
			m["p/helpers_test.go"] = "package p\nimport _ \"example.com/setup\"\nvar cases = []int{1}\n"
		}, []string{"TestX", "TestY", "TestZ"}},
		{"init added", func(m map[string]string) {
			m["p/helpers_test.go"] = helpers + "func init() {}\n"
		}, []string{"TestX", "TestY", "TestZ"}},
		{"TestMain changed", func(m map[string]string) {
			m["p/helpers_test.go"] = helpers + "func TestMain(m *testing.M) { m.Run() }\n"
		}, []string{"TestX", "TestY", "TestZ"}},
		{"build constraint changed", func(m map[string]string) {
			m["p/z_test.go"] = "//go:build linux\n\n" + z
		}, []string{"TestZ"}},
		{"nothing relevant", func(m map[string]string) {
			m["p/helpers_test.go"] = helpers + "// comment\n"
		}, nil},
	}
	for _, tc := range cases {
		head := map[string]string{}
		for k, v := range base {
			head[k] = v
		}
		tc.edit(head)
		got, err := changedPackageTests(base, head)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: changed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// #183 review: a test-file method on a type declared in non-test code is
// reached by method name (NewCalc().helper()), not by the type name.
func TestChangedPackageTestsFollowsMethodNames(t *testing.T) {
	base := map[string]string{
		"p/helpers_test.go": "package p\nfunc (c *Calc) helper() int { return 1 }\n",
		"p/x_test.go":       "package p\nimport \"testing\"\nfunc TestCalc(t *testing.T) { _ = NewCalc().helper() }\nfunc TestOther(t *testing.T) {}\n",
	}
	head := map[string]string{
		"p/helpers_test.go": "package p\nfunc (c *Calc) helper() int { return 2 }\n",
		"p/x_test.go":       base["p/x_test.go"],
	}
	got, err := changedPackageTests(base, head)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestCalc"}; !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
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
	t          *testing.T
	dir        string
	base       string
	baseOutput string
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
	// analyze through the CLI, as the workflow does, so the
	// base_failure_output.txt artifact is exercised too.
	r.baseOutput = filepath.Join(out, "base_failure_output.txt")
	stdout.Reset()
	if code := run([]string{"analyze", "-plan", planFile, "-results", out, "-base-output", r.baseOutput}, &stdout, &stderr); code != 0 {
		r.t.Fatalf("analyze exit %d: %s", code, stderr.String())
	}
	plan, err := readPlan(planFile)
	if err != nil {
		r.t.Fatal(err)
	}
	return plan, analyze(plan, out)
}

func (r *fixtureRepo) baseFailureOutput() string {
	r.t.Helper()
	data, err := os.ReadFile(r.baseOutput)
	if err != nil {
		r.t.Fatalf("base_failure_output.txt not written: %v", err)
	}
	return string(data)
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
	if got := r.baseFailureOutput(); !strings.Contains(got, "internal/calc.TestAddFromFixture (base: fail)") ||
		!strings.Contains(got, "Add(2, 3) = -1, want 5") || !strings.Contains(got, "pass=true") {
		t.Errorf("base_failure_output.txt missing the base failure:\n%s", got)
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
	if got := r.baseFailureOutput(); !strings.Contains(got, "compile failed on base") || !strings.Contains(got, "undefined: Sub") {
		t.Errorf("base_failure_output.txt missing the compile error:\n%s", got)
	}
}

// globFixtureTest passes only when testdata holds exactly the expected
// fixture, so a stale fixture left on base makes it fail there.
const globFixtureTest = `package %s

import (
	"path/filepath"
	"testing"
)

func TestOnlyCurrentFixture(t *testing.T) {
	got, err := filepath.Glob("testdata/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "new.txt" {
		t.Fatalf("fixtures = %%v, want only testdata/new.txt", got)
	}
}
`

// #182 review: a PR that deletes or renames a testdata fixture must have
// that fixture removed from the base overlay too. Otherwise base keeps the
// old file, the test fails there only because its inputs differ, and a
// test-only PR looks like a genuine repro.
func TestReproRemovesDeletedAndRenamedFixturesFromBase(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/testdata/old.txt", "old\n")
	r.write("internal/calc/testdata/gone.txt", "gone\n")
	r.base = r.commit("fixtures")
	r.git("checkout", "-q", "-b", "rename-fixture")
	r.git("mv", "internal/calc/testdata/old.txt", "internal/calc/testdata/new.txt")
	r.git("rm", "-q", "internal/calc/testdata/gone.txt")
	r.write("internal/calc/glob_test.go", fmt.Sprintf(globFixtureTest, "calc"))
	head := r.commit("rename fixture, add test")

	plan, outcome := r.pipeline(head)
	deleted := map[string]bool{}
	for _, f := range plan.Overlay {
		if f.Delete {
			deleted[f.Path] = true
		}
	}
	if !deleted["internal/calc/testdata/old.txt"] || !deleted["internal/calc/testdata/gone.txt"] {
		t.Fatalf("overlay = %+v, want deletions for the renamed and removed fixtures", plan.Overlay)
	}
	if outcome.Pass || outcome.Inconclusive {
		t.Fatalf("outcome = %+v, want a conclusive fail (test-only PR)", outcome)
	}
	if len(outcome.Tests) != 1 || outcome.Tests[0].Base != "pass" {
		t.Fatalf("base status = %+v, want pass (stale fixtures must be gone on base)", outcome.Tests)
	}
}

// #182 review: testdata at the repository root (package ".") must be
// overlaid too. The shell version built the pathspec "/testdata" there.
func TestReproOverlaysRootLevelTestdata(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("root.go", "package fixture\n")
	r.base = r.commit("root package")
	r.git("checkout", "-q", "-b", "root-fixture")
	r.write("testdata/new.txt", "new\n")
	r.write("root_test.go", fmt.Sprintf(globFixtureTest, "fixture"))
	head := r.commit("root test")

	plan, outcome := r.pipeline(head)
	if want := []string{"..TestOnlyCurrentFixture"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v", plan.Tests(), want)
	}
	overlaid := false
	for _, f := range plan.Overlay {
		overlaid = overlaid || (f.Path == "testdata/new.txt" && !f.Delete)
	}
	if !overlaid {
		t.Fatalf("overlay = %+v, want testdata/new.txt", plan.Overlay)
	}
	if outcome.Pass || outcome.Inconclusive || len(outcome.Tests) != 1 || outcome.Tests[0].Base != "pass" {
		t.Fatalf("outcome = %+v, want base pass (root fixture present on base) and a conclusive fail", outcome)
	}
}

// #183 review: a table-driven regression case added to a package-level var
// is planned and scored end to end.
func TestReproTableDrivenCaseAddedToPackageVar(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/table_test.go", `package calc

import "testing"

var addCases = []struct{ a, b, want int }{{0, 0, 0}}

func TestAddTable(t *testing.T) {
	for _, c := range addCases {
		if got := Add(c.a, c.b); got != c.want {
			t.Fatalf("Add(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
`)
	r.base = r.commit("table test")
	r.git("checkout", "-q", "-b", "table-case")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	src, err := os.ReadFile(filepath.Join(r.dir, "internal/calc/table_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	r.write("internal/calc/table_test.go", strings.Replace(string(src), "{{0, 0, 0}}", "{{0, 0, 0}, {2, 3, 5}}", 1))
	head := r.commit("fix Add, add case")

	plan, outcome := r.pipeline(head)
	if want := []string{"internal/calc.TestAddTable"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v", plan.Tests(), want)
	}
	if !outcome.Pass || !strings.Contains(outcome.BaseOutput, "Add(2, 3) = -1, want 5") {
		t.Fatalf("outcome = %+v, want a genuine pass", outcome)
	}
}

// #183 review: overlay files keep their git mode on base. A test-only PR
// whose test runs a new executable fixture must not fail on base with
// "permission denied" and look like a repro.
func TestReproOverlayKeepsExecutableFixtures(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	r := newFixtureRepo(t)
	r.git("checkout", "-q", "-b", "exec-fixture")
	r.write("internal/calc/testdata/run.sh", "#!/bin/sh\necho ok\n")
	if err := os.Chmod(filepath.Join(r.dir, "internal/calc/testdata/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.write("internal/calc/exec_test.go", `package calc

import (
	"os/exec"
	"testing"
)

func TestRunsFixture(t *testing.T) {
	out, err := exec.Command("./testdata/run.sh").CombinedOutput()
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("run.sh: %v %q", err, out)
	}
}
`)
	head := r.commit("exec fixture")

	plan, outcome := r.pipeline(head)
	var mode string
	for _, f := range plan.Overlay {
		if f.Path == "internal/calc/testdata/run.sh" {
			mode = f.Mode
		}
	}
	if mode != "100755" {
		t.Fatalf("overlay mode for run.sh = %q, want 100755", mode)
	}
	if outcome.Pass || len(outcome.Tests) != 1 || outcome.Tests[0].Base != "pass" {
		t.Fatalf("outcome = %+v, want base pass (fixture executable on base) and no repro", outcome)
	}
}

// #183 review: a regression case can be added to a golden/table test by
// adding only a fixture. The owning package's testdata-reading tests are
// planned, and a genuine fix scores as a repro.
func TestReproFixtureOnlyRegressionCase(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/golden_test.go", `package calc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoldenCases(t *testing.T) {
	files, _ := filepath.Glob(casesDir + "/*.txt")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var a, b, want int
		if _, err := fmt.Sscanf(string(raw), "%d+%d=%d", &a, &b, &want); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if got := Add(a, b); got != want {
			t.Fatalf("%s: Add(%d, %d) = %d, want %d", f, a, b, got, want)
		}
	}
}

func TestNoFixtures(t *testing.T) {}
`)
	// casesDir lives in another declaration, so the planner has to follow
	// the use, not just look at TestGoldenCases's own body.
	r.write("internal/calc/dirs_test.go", "package calc\n\nconst casesDir = \"testdata/golden\"\n")
	src, _ := os.ReadFile(filepath.Join(r.dir, "internal/calc/golden_test.go"))
	r.write("internal/calc/golden_test.go", strings.Replace(string(src), "import (\n", "import (\n\t\"fmt\"\n", 1))
	r.write("internal/calc/testdata/golden/zero.txt", "0+0=0\n")
	r.base = r.commit("golden test")
	r.git("checkout", "-q", "-b", "fixture-case")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	r.write("internal/calc/testdata/golden/two.txt", "2+3=5\n")
	head := r.commit("fix Add, add golden case")

	plan, outcome := r.pipeline(head)
	if want := []string{"internal/calc.TestGoldenCases"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v", plan.Tests(), want)
	}
	if !outcome.Pass || !strings.Contains(outcome.BaseOutput, "Add(2, 3) = -1, want 5") {
		t.Fatalf("outcome = %+v, want a genuine pass", outcome)
	}
}

// When the PR changes a test as well as a fixture, only the changed tests
// are planned; unchanged golden tests in that package pass on base and must
// not turn a genuine repro into a failure.
func TestReproFixtureWithChangedTestKeepsPlanNarrow(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/golden_test.go", "package calc\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestGoldenUnchanged(t *testing.T) { _, _ = os.ReadDir(\"testdata\") }\n")
	r.base = r.commit("golden test")
	r.git("checkout", "-q", "-b", "fix")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	r.write("internal/calc/add_test.go", fixtureTest)
	r.write("internal/calc/testdata/cases.txt", "2+3=5\n")
	head := r.commit("fix Add")

	plan, outcome := r.pipeline(head)
	if want := []string{"internal/calc.TestAddFromFixture"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v", plan.Tests(), want)
	}
	if !outcome.Pass {
		t.Fatalf("outcome = %+v, want pass", outcome)
	}
}

// #183 review e2e: a table case added in helpers_test.go is planned for the
// unchanged test in another file that uses it.
func TestReproTableInOtherFile(t *testing.T) {
	r := newFixtureRepo(t)
	r.write("internal/calc/table_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAddTable(t *testing.T) {\n\tfor _, c := range addCases {\n\t\tif got := Add(c.a, c.b); got != c.want {\n\t\t\tt.Fatalf(\"Add(%d, %d) = %d, want %d\", c.a, c.b, got, c.want)\n\t\t}\n\t}\n}\n")
	r.write("internal/calc/cases_test.go", "package calc\n\nvar addCases = []struct{ a, b, want int }{{0, 0, 0}}\n")
	r.base = r.commit("table")
	r.git("checkout", "-q", "-b", "case")
	r.write("internal/calc/calc.go", "package calc\n\nfunc Add(a, b int) int { return a + b }\n")
	r.write("internal/calc/cases_test.go", "package calc\n\nvar addCases = []struct{ a, b, want int }{{0, 0, 0}, {2, 3, 5}}\n")
	head := r.commit("fix")

	plan, outcome := r.pipeline(head)
	if want := []string{"internal/calc.TestAddTable"}; !reflect.DeepEqual(plan.Tests(), want) {
		t.Fatalf("planned tests = %v, want %v", plan.Tests(), want)
	}
	if !outcome.Pass {
		t.Fatalf("outcome = %+v, want pass", outcome)
	}
}

func TestTestdataOwner(t *testing.T) {
	for file, want := range map[string]string{
		"internal/calc/testdata/a.txt":      "internal/calc",
		"internal/calc/testdata/sub/b.json": "internal/calc",
		"testdata/x":                        ".",
		"a/testdata/b/testdata/c":           "a",
	} {
		if got := testdataOwner(file); got != want {
			t.Errorf("testdataOwner(%q) = %q, want %q", file, got, want)
		}
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
	if got := r.baseFailureOutput(); !strings.Contains(got, "no base failure output") {
		t.Errorf("base_failure_output.txt should be written with an explicit empty marker:\n%s", got)
	}
}
