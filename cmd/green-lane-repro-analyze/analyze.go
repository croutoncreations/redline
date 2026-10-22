package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Outcome is the scored repro result.
type Outcome struct {
	Pass         bool           `json:"pass"`
	Inconclusive bool           `json:"inconclusive"`
	Message      string         `json:"message"`
	TestIDs      []string       `json:"test_ids,omitempty"`
	Tests        []TestEvidence `json:"tests,omitempty"`
	BaseOutput   string         `json:"base_failure_output,omitempty"`
}

// TestEvidence records what one planned test did on each side.
type TestEvidence struct {
	ID   string `json:"id"`
	Base string `json:"base"` // pass, fail, skip or "" when it never ran
	Head string `json:"head"`
	// Optional marks an unchanged fixture reader: it need not fail on
	// base, but counts as a repro when it does.
	Optional bool `json:"optional,omitempty"`
}

// runResult is what one go test -json stream says about a package.
type runResult struct {
	tests map[string]string // top-level test name -> pass/fail/skip
	// runs counts terminal events per top-level name. A directory's
	// internal (package p) and external (package p_test) tests share one
	// test binary and one Package path, so both may define TestX; go test
	// -json then reports two "TestX" results that cannot be told apart.
	runs        map[string]int
	output      map[string]string // top-level test name -> its output
	buildFailed bool
	buildOutput string
	completed   bool
}

func analyze(plan Plan, resultsDir string) Outcome {
	if plan.Error != "" {
		return Outcome{Inconclusive: true, Message: "Repro plan failed: " + plan.Error}
	}
	if len(plan.Packages) == 0 {
		return Outcome{Inconclusive: true, Message: "No new or changed Go test functions in PR"}
	}
	if len(plan.UntrackedInputs) > 0 {
		return Outcome{Inconclusive: true, Message: fmt.Sprintf(
			"Tests appear to read changed files outside testdata/ that are not overlaid onto base: %s",
			strings.Join(plan.UntrackedInputs, ", "))}
	}

	var out Outcome
	var problems []string
	var baseOutput strings.Builder
	for i, pkg := range plan.Packages {
		base, err := parseResultFile(resultFile(resultsDir, "base", i))
		if err != nil {
			return Outcome{Inconclusive: true, Message: fmt.Sprintf("Base results for %s unusable: %v", pkg.Dir, err)}
		}
		if base.buildFailed {
			return Outcome{
				Inconclusive: true,
				Message:      fmt.Sprintf("Compile failed on base commit for %s: %s", goPackageArg(pkg.Dir), firstLines(base.buildOutput, 3)),
				BaseOutput:   fmt.Sprintf("=== %s (compile failed on base)\n%s", goPackageArg(pkg.Dir), base.buildOutput),
			}
		}
		head, err := parseResultFile(resultFile(resultsDir, "head", i))
		if err != nil {
			return Outcome{Inconclusive: true, Message: fmt.Sprintf("Head results for %s unusable: %v", pkg.Dir, err)}
		}
		if head.buildFailed {
			return Outcome{Inconclusive: true, Message: fmt.Sprintf("Compile failed on head commit for %s: %s", goPackageArg(pkg.Dir), firstLines(head.buildOutput, 3))}
		}
		optionalRepro := false
		for _, name := range pkg.run() {
			id := testID(pkg.Dir, name)
			required := !contains(pkg.Optional, name)
			if base.runs[name] > 1 || head.runs[name] > 1 {
				return Outcome{Inconclusive: true, Message: fmt.Sprintf(
					"%s: ambiguous, %d results on base and %d on head (same name in the internal and external test packages)",
					id, base.runs[name], head.runs[name])}
			}
			ev := TestEvidence{ID: id, Base: base.tests[name], Head: head.tests[name], Optional: !required}
			out.Tests = append(out.Tests, ev)
			// Keep the base output of every test that failed there, whatever
			// the verdict, so reviewers can see why it failed before.
			if ev.Base == "fail" {
				fmt.Fprintf(&baseOutput, "=== %s (base: fail)\n%s", id, base.output[name])
			}
			switch {
			case ev.Base == "" && required:
				problems = append(problems, id+": did not execute on base")
			case ev.Head == "":
				problems = append(problems, id+": did not execute on head")
			case ev.Head != "pass":
				problems = append(problems, fmt.Sprintf("%s: did not pass on head (status: %s)", id, ev.Head))
			case ev.Base == "fail":
				out.TestIDs = append(out.TestIDs, id)
				optionalRepro = optionalRepro || !required
			case required:
				problems = append(problems, fmt.Sprintf("%s: did not fail on base (status: %s)", id, ev.Base))
			}
		}
		if len(pkg.Tests) == 0 && !optionalRepro {
			problems = append(problems, fmt.Sprintf("%s: no test reading the changed testdata failed on base", goPackageArg(pkg.Dir)))
		}
	}

	out.BaseOutput = baseOutput.String()
	if len(problems) == 0 && len(out.TestIDs) == 0 {
		problems = append(problems, "no planned test failed on base")
	}
	if len(problems) > 0 {
		out.Message = strings.Join(problems, "; ")
		return out
	}
	out.Pass = true
	out.Message = fmt.Sprintf("Tests %v failed on base, passed on head", out.TestIDs)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func parseResultFile(file string) (runResult, error) {
	f, err := os.Open(file)
	if err != nil {
		return runResult{}, err
	}
	defer f.Close()
	return parseResults(f)
}

// parseResults reads a go test -json stream for one package. Build failures
// show up as "build-fail" events or a package failure with FailedBuild set.
func parseResults(r io.Reader) (runResult, error) {
	res := runResult{tests: map[string]string{}, runs: map[string]int{}, output: map[string]string{}}
	var build strings.Builder
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Action      string
			Test        string
			Output      string
			FailedBuild string
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			return res, fmt.Errorf("malformed JSON line %q", truncate(string(line), 120))
		}
		switch {
		case ev.Action == "build-output":
			build.WriteString(ev.Output)
		case ev.Action == "build-fail":
			res.buildFailed = true
		case ev.Test == "":
			if ev.Action == "output" && (strings.Contains(ev.Output, "[build failed]") || strings.Contains(ev.Output, "[setup failed]")) {
				res.buildFailed = true
				build.WriteString(ev.Output)
			}
			if ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip" {
				res.completed = true
				if ev.FailedBuild != "" {
					res.buildFailed = true
				}
			}
		default:
			top, _, sub := strings.Cut(ev.Test, "/")
			switch ev.Action {
			case "pass", "fail", "skip":
				if !sub {
					res.tests[top] = ev.Action
					res.runs[top]++
				}
			case "output":
				res.output[top] += ev.Output
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return res, err
	}
	res.buildOutput = build.String()
	if !res.completed && !res.buildFailed {
		return res, fmt.Errorf("package did not complete")
	}
	return res, nil
}

// baseFailureReport is the content of base_failure_output.txt. It is always
// written, so a missing artifact never reads as "nothing failed".
func baseFailureReport(o Outcome) string {
	header := fmt.Sprintf("green-lane repro: pass=%t inconclusive=%t\n%s\n\n", o.Pass, o.Inconclusive, o.Message)
	if o.BaseOutput == "" {
		return header + "(no test failed on the base commit; no base failure output)\n"
	}
	return header + o.BaseOutput
}

// writeOutputs prints GITHUB_OUTPUT lines. Messages are flattened to a single
// line so test output cannot inject extra outputs.
func writeOutputs(w io.Writer, o Outcome) {
	fmt.Fprintf(w, "pass=%t\n", o.Pass)
	fmt.Fprintf(w, "inconclusive=%t\n", o.Inconclusive)
	fmt.Fprintf(w, "message=%s\n", oneLine(o.Message))
	fmt.Fprintf(w, "test_ids=%s\n", oneLine(strings.Join(o.TestIDs, ",")))
}

func oneLine(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return truncate(s, 900)
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	if len(lines) == 0 || lines[0] == "" {
		return "(no build output)"
	}
	return strings.Join(lines, " | ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
