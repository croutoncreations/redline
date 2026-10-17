// Command green-lane-repro-analyze plans, runs and scores the green-lane
// "fails before, passes after" repro check.
//
// The workflow builds this binary from trusted base-branch code before any PR
// code runs, so a PR cannot change the code that decides which tests count or
// how their results are scored.
//
//	plan    -repo DIR -base SHA -head SHA -out plan.json
//	        Reads git objects only (no PR code runs). Prints has_tests=... .
//	run     -repo DIR -plan plan.json -out DIR
//	        Runs the planned tests on the merge-base (with the PR's test files
//	        and testdata overlaid) and on the head, in fresh worktrees.
//	        This executes PR code; its output is evidence, not a verdict.
//	analyze -plan plan.json -results DIR [-evidence FILE]
//	        Scores the results and prints GITHUB_OUTPUT lines
//	        (pass, inconclusive, message, test_ids).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: green-lane-repro-analyze plan|run|analyze [flags]")
		return 2
	}
	switch args[0] {
	case "plan":
		return cmdPlan(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stderr)
	case "analyze":
		return cmdAnalyze(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		return 2
	}
}

func cmdPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git checkout containing both commits")
	base := fs.String("base", "", "base commit SHA")
	head := fs.String("head", "", "head commit SHA")
	out := fs.String("out", "plan.json", "where to write the plan")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	plan, err := buildPlan(*repo, *base, *head)
	if err != nil {
		// Record the error in the plan so analyze reports it as inconclusive
		// instead of silently treating "no plan" as "no tests".
		plan = Plan{Base: *base, Head: *head, Error: err.Error()}
		fmt.Fprintf(stderr, "::warning::green-lane repro plan failed: %v\n", err)
	}
	if err := writePlan(*out, plan); err != nil {
		fmt.Fprintf(stderr, "write plan: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "has_tests=%t\n", plan.Error == "" && len(plan.Packages) > 0)
	return 0
}

func cmdRun(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git checkout containing both commits")
	planPath := fs.String("plan", "plan.json", "plan written by the plan subcommand")
	out := fs.String("out", ".", "directory for result files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	plan, err := readPlan(*planPath)
	if err != nil {
		fmt.Fprintf(stderr, "read plan: %v\n", err)
		return 1
	}
	if err := runRepro(*repo, plan, *out, stderr); err != nil {
		fmt.Fprintf(stderr, "run: %v\n", err)
		return 1
	}
	return 0
}

func cmdAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	planPath := fs.String("plan", "plan.json", "plan computed by trusted code")
	results := fs.String("results", ".", "directory holding the run subcommand's result files")
	evidence := fs.String("evidence", "", "optional path for a JSON evidence file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var outcome Outcome
	plan, err := readPlan(*planPath)
	if err != nil {
		outcome = Outcome{Inconclusive: true, Message: fmt.Sprintf("Could not read repro plan: %v", err)}
	} else {
		outcome = analyze(plan, *results)
	}
	writeOutputs(stdout, outcome)
	if *evidence != "" {
		if err := writeJSON(*evidence, outcome); err != nil {
			fmt.Fprintf(stderr, "write evidence: %v\n", err)
		}
	}
	return 0
}
