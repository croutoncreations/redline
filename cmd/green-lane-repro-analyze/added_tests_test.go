package main

import (
	"reflect"
	"strings"
	"testing"
)

// A PR that adds one regression test to an existing test file must be judged
// on that test alone. Collecting every test in the file would require
// unchanged tests, which correctly pass on base, to fail there too, so the
// repro rule would fail for an honest regression test.
func TestAddedTestIDsIgnoresUnchangedTestsInTheSameFile(t *testing.T) {
	diff := `diff --git a/internal/widget/widget_test.go b/internal/widget/widget_test.go
index 1111111..2222222 100644
--- a/internal/widget/widget_test.go
+++ b/internal/widget/widget_test.go
@@ -40,0 +41,6 @@ func TestExistingBehaviour(t *testing.T) {
+
+func TestRegressionForIssue42(t *testing.T) {
+	if Widget() != 2 {
+		t.Fatal("regressed")
+	}
+}
`
	got := addedTestIDs(strings.NewReader(diff))
	want := []addedTest{{File: "internal/widget/widget_test.go", Name: "TestRegressionForIssue42"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addedTestIDs = %+v, want %+v", got, want)
	}
}

func TestAddedTestIDsAcrossFilesSkipsRemovedAndContextLines(t *testing.T) {
	diff := `diff --git a/a/a_test.go b/a/a_test.go
--- a/a/a_test.go
+++ b/a/a_test.go
@@ -1,3 +1,3 @@
 func TestKept(t *testing.T) {}
-func TestRemoved(t *testing.T) {}
+func TestRenamedInPlace(t *testing.T) {}
diff --git a/b/new_test.go b/b/new_test.go
new file mode 100644
--- /dev/null
+++ b/b/new_test.go
@@ -0,0 +1,4 @@
+package b
+func TestNewFileOne(t *testing.T) {}
+func TestNewFileTwo(t *testing.T) {}
+func helperNotATest(t *testing.T) {}
+func Testlowercase(t *testing.T) {}
diff --git a/c/gone_test.go b/c/gone_test.go
deleted file mode 100644
--- a/c/gone_test.go
+++ /dev/null
@@ -1 +0,0 @@
-func TestDeleted(t *testing.T) {}
diff --git a/d/main.go b/d/main.go
--- a/d/main.go
+++ b/d/main.go
@@ -1 +1 @@
+func TestInNonTestFile(t *testing.T) {}
`
	got := addedTestIDs(strings.NewReader(diff))
	want := []addedTest{
		{File: "a/a_test.go", Name: "TestRenamedInPlace"},
		{File: "b/new_test.go", Name: "TestNewFileOne"},
		{File: "b/new_test.go", Name: "TestNewFileTwo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addedTestIDs = %+v, want %+v", got, want)
	}
}

// An added line whose content is "++ b/elsewhere_test.go" appears in the diff
// as "+++ b/elsewhere_test.go", exactly like a file header. Inside a hunk it
// must stay content, so the next test is still attributed to x_test.go.
func TestAddedTestIDsDoesNotTreatAddedContentAsAHeader(t *testing.T) {
	diff := `diff --git a/x/x_test.go b/x/x_test.go
--- a/x/x_test.go
+++ b/x/x_test.go
@@ -1 +1,3 @@
+++ b/elsewhere_test.go
+func TestStillInX(t *testing.T) {}
+func TestGeneric[T any](t *testing.T) {}
`
	got := addedTestIDs(strings.NewReader(diff))
	want := []addedTest{{File: "x/x_test.go", Name: "TestStillInX"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addedTestIDs = %+v, want %+v", got, want)
	}
}

// Go's naming rule decides what is a test: TestMain is the harness, a
// lowercase letter after "Test" makes an ordinary function, and methods never
// run as tests. Test_foo, Test1 and plain Test are real tests.
func TestAddedTestIDsFollowsGoTestNamingRules(t *testing.T) {
	diff := `diff --git a/p/p_test.go b/p/p_test.go
--- a/p/p_test.go
+++ b/p/p_test.go
@@ -0,0 +1,7 @@
+func TestMain(m *testing.M) { os.Exit(m.Run()) }
+func Testable(t *testing.T) {}
+func (s *suite) TestMethod(t *testing.T) {}
+func Test_underscore(t *testing.T) {}
+func Test1(t *testing.T) {}
+func Test(t *testing.T) {}
+func TestRegression(t *testing.T) {}
`
	got := addedTestIDs(strings.NewReader(diff))
	want := []addedTest{
		{File: "p/p_test.go", Name: "Test_underscore"},
		{File: "p/p_test.go", Name: "Test1"},
		{File: "p/p_test.go", Name: "Test"},
		{File: "p/p_test.go", Name: "TestRegression"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addedTestIDs = %+v, want %+v", got, want)
	}
}
