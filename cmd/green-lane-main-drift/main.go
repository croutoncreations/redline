// Command green-lane-main-drift is the merge-queue half of Green Lane's
// Main Drift rule. It runs as the required "main-drift" check on every
// merge_group CI run and fails when main changed a denied path since a
// queued PR's own merge base, so a PR that was evaluated against an older
// main can't land on top of sensitive changes it was never reviewed against.
//
// From the merge_group payload it takes base_sha, the commit the PR lands on
// (main plus any entries ahead of it), and the PR number from head_ref
// (refs/heads/gh-readonly-queue/<base>/pr-<n>-<sha>; that SHA is the base,
// not the PR head). The PR head comes from the API with the read-only
// workflow token, and git metadata from the local checkout. For other events
// it passes, so the check is always reported.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/greenlane"
)

func main() {
	payload, _ := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	lookup := apiPRHead(os.Getenv("GITHUB_API_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN"))
	if err := run(os.Getenv("GITHUB_EVENT_NAME"), payload, git, lookup); err != nil {
		fmt.Fprintln(os.Stderr, "::error::"+err.Error())
		os.Exit(1)
	}
}

type gitFunc func(args ...string) (string, error)

// prHeadFunc returns the current head SHA of pull request n.
type prHeadFunc func(n int) (string, error)

func apiPRHead(api, repo, token string) prHeadFunc {
	return func(n int) (string, error) {
		if api == "" || repo == "" || token == "" {
			return "", fmt.Errorf("missing GITHUB_API_URL, GITHUB_REPOSITORY or GITHUB_TOKEN")
		}
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/repos/%s/pulls/%d", api, repo, n), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("GitHub API error %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		if err != nil {
			return "", err
		}
		var pr struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if err := json.Unmarshal(body, &pr); err != nil {
			return "", err
		}
		if pr.Number != n {
			return "", fmt.Errorf("API returned PR %d for %d", pr.Number, n)
		}
		return pr.Head.SHA, nil
	}
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	// Keep NUL-separated output intact; only trim the line ending.
	return strings.TrimRight(string(out), "\n"), err
}

var (
	queueRef = regexp.MustCompile(`^refs/heads/gh-readonly-queue/.+/pr-([0-9]+)-[0-9a-f]{40}$`)
	fullSHA  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func run(event string, payload []byte, git gitFunc, prHead prHeadFunc) error {
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
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return fmt.Errorf("main-drift: bad PR number in %q", p.MergeGroup.HeadRef)
	}
	head, err := prHead(n)
	if err != nil || !fullSHA.MatchString(head) {
		return fmt.Errorf("main-drift: cannot read PR #%d's head: %v", n, err)
	}
	// The queued merge must contain this head, or the PR moved: fail closed.
	if _, err := git("fetch", "--no-tags", "--quiet", "origin", landing, head); err != nil {
		return fmt.Errorf("main-drift: cannot fetch the landing commit and PR head: %v", err)
	}
	if _, err := git("merge-base", "--is-ancestor", head, "HEAD"); err != nil {
		return fmt.Errorf("main-drift: PR #%d's head %s is not in this merge group", n, head[:12])
	}
	mergeBase, err := git("merge-base", landing, head)
	if err != nil || !fullSHA.MatchString(mergeBase) {
		return fmt.Errorf("main-drift: cannot find the PR's merge base: %v", err)
	}
	// -z: NUL-separated, unquoted paths, so unusual filenames can't slip past
	// denied-path matching as git's quoted form ("a/\303\251.go").
	changed, err := git("diff", "--name-only", "--no-renames", "-z", mergeBase, landing)
	if err != nil {
		return fmt.Errorf("main-drift: cannot list what main changed: %v", err)
	}
	var files []string
	for _, f := range strings.Split(changed, "\x00") {
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
