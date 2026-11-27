package greenlane

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The queue gate re-decides, when the merge queue tests a PR, whether it may
// land. Green Lane checks a PR when it enqueues it, but reviews, threads and
// hold labels can change while it waits, and the live worker only re-checks
// on its own schedule. The gate runs as a required merge_group check, after
// every other required check, so the queue cannot merge without a fresh
// verdict.

// QueueConfig is the merge queue's own configuration. The gate's guarantees
// assume a squash queue that builds one entry at a time and only merges
// entries whose own checks passed.
type QueueConfig struct {
	MergeMethod     string // SQUASH
	MergingStrategy string // ALLGREEN
	MaxEntriesBuild int    // 1
}

// QueueGateInput is the trusted evidence for one merge-group run.
type QueueGateInput struct {
	// From the merge_group payload.
	BaseSHA      string // the commit the PR lands on
	GroupHeadSHA string // the commit the queue is testing and will merge
	HeadRef      string // refs/heads/gh-readonly-queue/<base>/pr-<n>-<sha>

	// From the API.
	PRNumber    int
	PRHead      string
	EntryHead   string   // the queue entry's head commit
	EntryBase   string   // the queue entry's base commit
	GroupParent []string // parents of GroupHeadSHA
	Queue       QueueConfig
	// The files the group commit changes on BaseSHA (both paths of renames).
	LandedFiles      []string
	LandedFilesKnown bool
	// Who added the PR to the queue. A hand enqueue by one of
	// HumanEnqueuerIDs is itself the authorization; anyone else, Green
	// Lane's App included, must pass every Green Lane rule.
	EnqueuerType     string // GraphQL __typename
	EnqueuerID       int64  // User databaseId
	HumanEnqueuerIDs []int64
	Labels           []string
	HoldLabels       []string

	// The full evaluator run on PRHead (with its file list), and Main Drift
	// from the PR's merge base to BaseSHA.
	Evaluation        *EvaluationResult
	PRFiles           []PRFile
	LandingDrift      []string
	LandingDriftKnown bool
	DeniedPaths       []string
}

var queueRefPattern = regexp.MustCompile(`^refs/heads/gh-readonly-queue/.+/pr-([0-9]+)-[0-9a-f]{40}$`)

// EnableMergeQueue switches c to merge-queue mode. The queue gate becomes a
// required check, so a PR whose CI predates it can't be enqueued (GitHub
// would refuse); requiring it here skips such PRs cleanly instead.
func (c *Config) EnableMergeQueue() {
	c.MergeQueue = true
	for _, name := range c.RequiredChecks {
		if name == QueueGateCheck {
			return
		}
	}
	if src, ok := c.CheckSources["go"]; ok {
		sources := map[string]CheckSource{}
		for k, v := range c.CheckSources {
			sources[k] = v
		}
		sources[QueueGateCheck] = src
		c.CheckSources = sources
	}
	c.RequiredChecks = append(append([]string(nil), c.RequiredChecks...), QueueGateCheck)
}

// QueueGateCheck is the required CI job that runs QueueGate.
const QueueGateCheck = "green-lane-queue"

// QueueRefPR parses the PR number from a merge-queue ref. The SHA in the ref
// is the base the group was built on, never the PR head.
func QueueRefPR(ref string) (int, bool) {
	m := queueRefPattern.FindStringSubmatch(ref)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// QueueGate returns nil when the queued PR may merge, or the reason it may
// not.
func QueueGate(in QueueGateInput) error {
	if !fullHead.MatchString(in.BaseSHA) || !fullHead.MatchString(in.GroupHeadSHA) {
		return fmt.Errorf("merge_group base_sha/head_sha are not full SHAs")
	}
	n, ok := QueueRefPR(in.HeadRef)
	if !ok || n != in.PRNumber {
		return fmt.Errorf("merge_group head_ref %q does not name PR #%d", in.HeadRef, in.PRNumber)
	}
	q := in.Queue
	if q.MergeMethod != "SQUASH" || q.MergingStrategy != "ALLGREEN" || q.MaxEntriesBuild != 1 {
		return fmt.Errorf("merge queue must be squash, only-merge-non-failing (ALLGREEN), one entry at a time; got %s/%s/%d", q.MergeMethod, q.MergingStrategy, q.MaxEntriesBuild)
	}
	// Bind this run to PR #n's queue entry: GitHub built the group commit
	// for this entry, as one squash commit directly on the base.
	if !fullHead.MatchString(in.PRHead) || in.EntryHead != in.GroupHeadSHA || in.EntryBase != in.BaseSHA ||
		len(in.GroupParent) != 1 || in.GroupParent[0] != in.BaseSHA {
		return fmt.Errorf("this merge group is not PR #%d's current queue entry", in.PRNumber)
	}
	if !in.LandingDriftKnown {
		return fmt.Errorf("could not list what main changed under PR #%d", in.PRNumber)
	}
	if sensitive := SensitiveDrift(in.LandingDrift, in.DeniedPaths); len(sensitive) > 0 {
		return fmt.Errorf("main changed sensitive paths since PR #%d's base; update the branch and get a fresh review: %s", in.PRNumber, strings.Join(sensitive, ", "))
	}
	if !in.LandedFilesKnown {
		return fmt.Errorf("could not list what PR #%d's merge commit changes", in.PRNumber)
	}
	if sensitive := SensitiveDrift(in.LandedFiles, in.DeniedPaths); len(sensitive) > 0 {
		return fmt.Errorf("PR #%d's merge commit changes sensitive paths: %s", in.PRNumber, strings.Join(sensitive, ", "))
	}
	if in.EnqueuerType == "" {
		return fmt.Errorf("could not tell who queued PR #%d", in.PRNumber)
	}
	if in.EnqueuerType == "User" {
		for _, id := range in.HumanEnqueuerIDs {
			if id > 0 && id == in.EnqueuerID {
				return nil // queued by hand: only the path guards apply
			}
		}
	}
	for _, l := range in.Labels {
		for _, h := range in.HoldLabels {
			if strings.EqualFold(l, h) {
				return fmt.Errorf("PR #%d has the %s label", in.PRNumber, l)
			}
		}
	}
	e := in.Evaluation
	if e == nil || e.PRNumber != in.PRNumber || e.HeadSHA != in.PRHead || len(e.Rules) == 0 {
		return fmt.Errorf("no Green Lane evaluation of PR #%d at %s", in.PRNumber, in.PRHead)
	}
	var failed []string
	for _, r := range e.Rules {
		if !r.Pass || r.Inconclusive {
			failed = append(failed, r.Name+": "+r.Message)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("PR #%d no longer qualifies for Green Lane: %s", in.PRNumber, strings.Join(failed, "; "))
	}
	// What lands must be what was evaluated: the group commit changes
	// exactly the PR's files.
	if want, got := fileSet(prPaths(in.PRFiles)), fileSet(in.LandedFiles); len(want) == 0 || want != got {
		return fmt.Errorf("PR #%d's merge commit changes different files from the evaluated PR", in.PRNumber)
	}
	return nil
}

func prPaths(files []PRFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Filename)
		if f.PreviousFilename != "" {
			out = append(out, f.PreviousFilename)
		}
	}
	return out
}

func fileSet(paths []string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\x00")
}
