package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// addedTest is a top-level Go test function introduced by the PR diff.
type addedTest struct {
	File string
	Name string
}

// addedTestFunc matches an added line declaring a non-generic top-level
// function named Test*. Generic functions and methods cannot be tests, so
// "TestX[T any]" and "func (s *suite) TestX" are skipped. isTestName applies
// the rest of go test's naming rule.
var addedTestFunc = regexp.MustCompile(`^\+func (Test[A-Za-z0-9_]*)\(`)

// isTestName reports whether go test would run name as a test: "Test" alone,
// or "Test" followed by anything that does not start with a lowercase letter
// (TestFoo, Test_foo, Test1). TestMain is the package's test harness, not a
// test, and is never a repro candidate.
func isTestName(name string) bool {
	if name == "TestMain" {
		return false
	}
	rest := strings.TrimPrefix(name, "Test")
	return rest == "" || rest[0] < 'a' || rest[0] > 'z'
}

// addedTestIDs reads a unified diff (git diff base head) and returns the test
// functions declared on added lines of *_test.go files, in diff order. Only
// added lines count: a test that already existed on base is unchanged by the
// PR and must not be required to fail there.
func addedTestIDs(diff io.Reader) []addedTest {
	var (
		found   []addedTest
		seen    = map[addedTest]bool{}
		current string
		inHunk  bool
	)
	scanner := bufio.NewScanner(diff)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current, inHunk = "", false
		case !inHunk && strings.HasPrefix(line, "+++ "):
			// The file header is only read before the first hunk, so an added
			// line whose content begins with "++ " is never mistaken for it.
			current = ""
			if path, ok := strings.CutPrefix(strings.TrimSuffix(line[4:], "\t"), "b/"); ok && strings.HasSuffix(path, "_test.go") {
				current = path
			}
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && current != "":
			if match := addedTestFunc.FindStringSubmatch(line); match != nil && isTestName(match[1]) {
				test := addedTest{File: current, Name: match[1]}
				if !seen[test] {
					seen[test] = true
					found = append(found, test)
				}
			}
		}
	}
	return found
}

// runAddedTests implements "green-lane-repro-analyze added-tests": it reads a
// unified diff on stdin and writes one "file<TAB>TestName" line per added test.
func runAddedTests(stdin io.Reader, stdout io.Writer) error {
	for _, test := range addedTestIDs(stdin) {
		if _, err := fmt.Fprintf(stdout, "%s\t%s\n", test.File, test.Name); err != nil {
			return err
		}
	}
	return nil
}
