package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/domain"
)

// exitNotAttributable distinguishes "this branch is not ours" from errors, so
// maintenance agents can skip a PR without treating it as a failure.
const exitNotAttributable = 3

// runAttribute prints whether a head branch was created by a finished
// standard Redline run in a repository. Exit 0: attributable; 3: not
// attributable; 1: error.
func runAttribute(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run attribute", flag.ContinueOnError)
	flags.SetOutput(stderr)
	branch := flags.String("branch", "", "pull request head branch")
	repo := flags.String("repo", "", "repository directory (default: git top level of the current directory)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *branch == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: redline run attribute --branch BRANCH [--repo DIR]")
		return 1
	}
	repository, err := repositoryArgument(*repo)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	path := "/v1/runs/attribution?branch=" + url.QueryEscape(*branch) + "&repository=" + url.QueryEscape(repository)
	var attribution domain.RunAttribution
	if err := client.Do(context.Background(), http.MethodGet, path, nil, &attribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, attribution)
	if !attribution.Attributable {
		return exitNotAttributable
	}
	return 0
}

// runLease acquires or releases a branch lease for the current run.
func runLease(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "acquire" && args[0] != "release") {
		fmt.Fprintln(stderr, "usage: redline lease <acquire|release> --branch BRANCH [--repo DIR] [--run RUN_ID]")
		return 1
	}
	flags := flag.NewFlagSet("lease "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	branch := flags.String("branch", "", "branch to lease")
	repo := flags.String("repo", "", "repository directory (default: git top level of the current directory)")
	runID := flags.String("run", os.Getenv("REDLINE_RUN_ID"), "run that holds the lease (default: $REDLINE_RUN_ID)")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if *branch == "" || *runID == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "--branch and --run (or REDLINE_RUN_ID) are required")
		return 1
	}
	repository, err := repositoryArgument(*repo)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if args[0] == "acquire" {
		var lease domain.BranchLease
		if err := client.Do(context.Background(), http.MethodPost, "/v1/branch-leases", map[string]string{
			"repository": repository, "branch": *branch, "run_id": *runID,
		}, &lease); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, lease)
		return 0
	}
	path := "/v1/branch-leases?repository=" + url.QueryEscape(repository) + "&branch=" + url.QueryEscape(*branch) +
		"&run_id=" + url.QueryEscape(*runID)
	if err := client.Do(context.Background(), http.MethodDelete, path, nil, nil); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, map[string]any{"released": true, "repository": repository, "branch": *branch, "run_id": *runID})
	return 0
}

// repositoryArgument returns an absolute repository path, defaulting to the
// git top level of the current directory.
func repositoryArgument(repo string) (string, error) {
	if repo != "" {
		absolute, err := filepath.Abs(expandHome(repo))
		if err != nil {
			return "", fmt.Errorf("resolve --repo: %w", err)
		}
		return absolute, nil
	}
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	top, err := gitTopLevel(context.Background(), directory)
	if err != nil {
		return "", fmt.Errorf("%w; pass --repo", err)
	}
	return top, nil
}
