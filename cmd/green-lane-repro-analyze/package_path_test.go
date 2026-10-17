package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoTestPackagePathsMustBeDotSlash reproduces the shadow-gate bug where
// package dirs were passed to `go test` as bare paths like "internal/foo".
// Go treats those as import paths, emits Action:build-fail / [setup failed],
// and the workflow grepped that as "Compile failed on base commit" for every PR.
func TestGoTestPackagePathsMustBeDotSlash(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	dir := t.TempDir()
	pkg := filepath.Join(dir, "internal", "demo")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.local/repro\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "demo.go"), []byte("package demo\n\nfunc Value() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "demo_test.go"), []byte("package demo\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 1 {\n\t\tt.Fatal(\"want 1\")\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runJSON := func(pkgArg string) (stdout string, exitCode int) {
		t.Helper()
		cmd := exec.Command("go", "test", "-count=1", "-json", "-run", "^TestValue$", pkgArg)
		cmd.Dir = dir
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		if err == nil {
			return out.String(), 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), ee.ExitCode()
		}
		t.Fatalf("go test %s: %v\n%s", pkgArg, err, out.String())
		return "", -1
	}

	bareOut, bareExit := runJSON("internal/demo")
	if bareExit == 0 {
		t.Fatalf("bare path unexpectedly succeeded:\n%s", bareOut)
	}
	if !strings.Contains(bareOut, `"Action":"build-fail"`) && !strings.Contains(bareOut, "[setup failed]") {
		t.Fatalf("bare path should report build-fail/setup failed (the false compile failure), got:\n%s", bareOut)
	}

	dotOut, dotExit := runJSON("./internal/demo")
	if dotExit != 0 {
		t.Fatalf("./ path should run tests, exit=%d:\n%s", dotExit, dotOut)
	}
	if strings.Contains(dotOut, `"Action":"build-fail"`) || strings.Contains(dotOut, "[setup failed]") {
		t.Fatalf("./ path should not look like a compile failure:\n%s", dotOut)
	}
	var sawPass bool
	for _, line := range strings.Split(dotOut, "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Test == "TestValue" && ev.Action == "pass" {
			sawPass = true
		}
	}
	if !sawPass {
		t.Fatalf("expected TestValue pass event with ./ path, got:\n%s", dotOut)
	}
}

// TestBaseOverlayNeedsTestdata proves that copying only *_test.go onto the base
// commit leaves fixtures missing; overlay must include package testdata/.
func TestBaseOverlayNeedsTestdata(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	root := t.TempDir()
	base := filepath.Join(root, "base")
	head := filepath.Join(root, "head")
	for _, d := range []string{base, head} {
		pkg := filepath.Join(d, "widget")
		if err := os.MkdirAll(filepath.Join(pkg, "testdata"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module example.local/overlay\n\ngo 1.22\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Base production code returns stale value "old".
		if err := os.WriteFile(filepath.Join(pkg, "widget.go"), []byte("package widget\n\nfunc Name() string { return \"old\" }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	testSrc := `package widget

import (
	"os"
	"testing"
)

func TestNameMatchesFixture(t *testing.T) {
	want, err := os.ReadFile("testdata/want.txt")
	if err != nil {
		t.Fatal(err)
	}
	if Name() != string(want) {
		t.Fatalf("got %q want %q", Name(), want)
	}
}
`
	if err := os.WriteFile(filepath.Join(head, "widget", "widget_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "widget", "testdata", "want.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Head also ships the fix so the same test passes there.
	if err := os.WriteFile(filepath.Join(head, "widget", "widget.go"), []byte("package widget\n\nfunc Name() string { return \"new\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(dir string) (out string, code int) {
		t.Helper()
		cmd := exec.Command("go", "test", "-count=1", "-json", "-run", "^TestNameMatchesFixture$", "./widget")
		cmd.Dir = dir
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		if err == nil {
			return buf.String(), 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return buf.String(), ee.ExitCode()
		}
		t.Fatalf("go test: %v\n%s", err, buf.String())
		return "", -1
	}

	// Mimic the old bug: copy only the test file onto base (no testdata).
	testsOnly := filepath.Join(root, "base-tests-only")
	if err := copyTree(base, testsOnly); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testsOnly, "widget", "widget_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	onlyOut, onlyCode := run(testsOnly)
	if onlyCode == 0 {
		t.Fatalf("test-only overlay should fail without testdata, got success:\n%s", onlyOut)
	}
	if !strings.Contains(onlyOut, "testdata/want.txt") && !strings.Contains(onlyOut, "no such file") {
		// Failure mode may be a Fatal with the read error.
		if !strings.Contains(onlyOut, `"Action":"fail"`) {
			t.Fatalf("expected fixture-missing failure, got:\n%s", onlyOut)
		}
	}

	// Correct overlay: tests + testdata on base → genuine fails-before.
	withData := filepath.Join(root, "base-with-testdata")
	if err := copyTree(base, withData); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(withData, "widget", "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withData, "widget", "widget_test.go"), []byte(testSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withData, "widget", "testdata", "want.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseOut, baseCode := run(withData)
	if baseCode == 0 {
		t.Fatalf("base with overlay should fail (want new, got old):\n%s", baseOut)
	}
	if !testAction(baseOut, "TestNameMatchesFixture", "fail") {
		t.Fatalf("want genuine test fail on base, got:\n%s", baseOut)
	}

	headOut, headCode := run(head)
	if headCode != 0 {
		t.Fatalf("head should pass:\n%s", headOut)
	}
	if !testAction(headOut, "TestNameMatchesFixture", "pass") {
		t.Fatalf("want pass on head, got:\n%s", headOut)
	}
}

func testAction(jsonOut, testName, action string) bool {
	for _, line := range strings.Split(jsonOut, "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Test == testName && ev.Action == action {
			return true
		}
	}
	return false
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
