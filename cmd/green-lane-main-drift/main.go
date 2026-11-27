// Command green-lane-main-drift is the merge-queue half of Green Lane's
// Main Drift rule. It runs as the required "main-drift" check on every
// merge_group CI run and fails when main changed a denied path since a
// queued PR's own merge base, so a PR that was evaluated against an older
// main can't land on top of sensitive changes it was never reviewed against.
//
// It uses the documented merge_group payload, not the shape of GitHub's
// merge-group commit: base_sha is the commit the PR lands on (main plus any
// entries ahead of it), and head_ref names the PR head as
// refs/heads/gh-readonly-queue/<base>/pr-<n>-<head sha>. Git metadata comes
// from the local checkout, so no API token is needed. For other events it
// passes, so the check is always reported.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/croutoncreations/redline/internal/greenlane"
)

func main() {
	payload, _ := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err := run(os.Getenv("GITHUB_EVENT_NAME"), payload, git); err != nil {
		fmt.Fprintln(os.Stderr, "::error::"+err.Error())
		os.Exit(1)
	}
}

type gitFunc func(args ...string) (string, error)

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

var (
	queueRef = regexp.MustCompile(`^refs/heads/gh-readonly-queue/[^/]+(/[^/]+)*/pr-[0-9]+-([0-9a-f]{40})$`)
	fullSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func run(event string, payload []byte, git gitFunc) error {
	if event != "merge_group" {
		fmt.Println("main-drift: not a merge-queue run; nothing to check")
		return nil
	}
	var p struct {
		MergeGroup struct {
			BaseSHA string `json:"base_sha"`
			HeadRef string `json:"head_ref"`
		} `json:"merge_group"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("main-drift: unreadable merge_group payload: %v", err)
	}
	landing := p.MergeGroup.BaseSHA
	m := queueRef.FindStringSubmatch(p.MergeGroup.HeadRef)
	if !fullSHA.MatchString(landing) || m == nil {
		return fmt.Errorf("main-drift: unexpected merge_group payload (base_sha %q, head_ref %q)", landing, p.MergeGroup.HeadRef)
	}
	prHead := m[2]
	// The PR head and its merge base must be in the checkout.
	if _, err := git("fetch", "--no-tags", "--quiet", "origin", landing, prHead); err != nil {
		return fmt.Errorf("main-drift: cannot fetch the landing commit and PR head: %v", err)
	}
	mergeBase, err := git("merge-base", landing, prHead)
	if err != nil || !fullSHA.MatchString(mergeBase) {
		return fmt.Errorf("main-drift: cannot find the PR's merge base: %v", err)
	}
	changed, err := git("diff", "--name-only", "--no-renames", mergeBase, landing)
	if err != nil {
		return fmt.Errorf("main-drift: cannot list what main changed: %v", err)
	}
	var files []string
	for _, f := range strings.Split(changed, "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	if sensitive := greenlane.SensitiveDrift(files, greenlane.DefaultDeniedPaths()); len(sensitive) > 0 {
		return fmt.Errorf("main-drift: main changed sensitive paths since this PR's base (%s); remove it from the queue, update the branch and get a fresh review: %s",
			mergeBase[:12], strings.Join(sensitive, ", "))
	}
	fmt.Printf("main-drift: OK, main changed %d files since this PR's base %s, none sensitive\n", len(files), mergeBase[:12])
	return nil
}
