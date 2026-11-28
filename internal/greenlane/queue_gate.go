package greenlane

import (
	"fmt"
	"regexp"
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
	// What the group commit changes on BaseSHA: paths (both sides of
	// renames) for the denied-path check, and per-file blobs.
	LandedFiles      []string
	Landed           []FileChange
	LandedFilesKnown bool
	// Who added the PR to the queue. Only two enqueuers are accepted: a
	// person in HumanEnqueuerIDs, whose hand enqueue is the authorization,
	// or the Green Lane App (a Bot in GreenLaneBots), which audits
	// repository writers before it enqueues and must pass every rule here.
	// Anyone else is rejected: a newly added writer could otherwise queue a
	// PR and pass the rules, which don't re-audit writers.
	EnqueuerType     string // GraphQL __typename
	EnqueuerLogin    string
	EnqueuerID       int64 // User databaseId
	HumanEnqueuerIDs []int64
	GreenLaneBots    []string
	Labels           []string
	HoldLabels       []string

	// The full evaluator run on PRHead, the PR's own changes from its merge
	// base to PRHead (with blobs), and Main Drift from that merge base to
	// BaseSHA.
	Evaluation        *EvaluationResult
	PRChanges         []FileChange
	PRChangesKnown    bool
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
	if src, ok := c.CheckSources["go"]; ok {
		sources := map[string]CheckSource{}
		for k, v := range c.CheckSources {
			sources[k] = v
		}
		sources[QueueGateCheck] = src
		c.CheckSources = sources
	}
	for _, name := range c.RequiredChecks {
		if name == QueueGateCheck {
			return
		}
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
	switch {
	case in.HumanEnqueue():
		return nil // queued by hand: only the path and binding guards apply
	case in.GreenLaneEnqueue():
	case in.EnqueuerType == "":
		return fmt.Errorf("could not tell who queued PR #%d", in.PRNumber)
	default:
		return fmt.Errorf("PR #%d was queued by %s %q, which is neither Jon nor Green Lane", in.PRNumber, in.EnqueuerType, in.EnqueuerLogin)
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
	// What lands must be what was evaluated: the merge commit changes the
	// same files, the same way, as the PR's current head, and for every file
	// main hasn't also changed since the PR's base, to the same content. That
	// catches a queue entry built from an older head. Files both sides
	// changed are merged by the queue, as for any reviewed PR on a moved main.
	if !in.PRChangesKnown || len(in.PRChanges) == 0 || !landsEvaluated(in.PRChanges, in.Landed, in.LandingDrift) {
		return fmt.Errorf("PR #%d's merge commit does not land the evaluated head's changes", in.PRNumber)
	}
	return nil
}

// HumanEnqueue reports whether a listed person queued the PR by hand.
func (in QueueGateInput) HumanEnqueue() bool {
	if in.EnqueuerType != "User" {
		return false
	}
	for _, id := range in.HumanEnqueuerIDs {
		if id > 0 && id == in.EnqueuerID {
			return true
		}
	}
	return false
}

// GreenLaneEnqueue reports whether the Green Lane App queued the PR.
func (in QueueGateInput) GreenLaneEnqueue() bool {
	if in.EnqueuerType != "Bot" {
		return false
	}
	for _, login := range in.GreenLaneBots {
		if login != "" && login == in.EnqueuerLogin {
			return true
		}
	}
	return false
}

// FileChange is one file in a GitHub compare: path, rename source, status
// and resulting blob.
type FileChange struct {
	Path, Previous, Status, Blob string
}

func landsEvaluated(pr, landed []FileChange, drift []string) bool {
	if len(pr) != len(landed) {
		return false
	}
	moved := map[string]bool{}
	for _, p := range drift {
		moved[p] = true
	}
	got := map[string]FileChange{}
	for _, f := range landed {
		if _, dup := got[f.Path]; dup {
			return false
		}
		got[f.Path] = f
	}
	for _, want := range pr {
		f, ok := got[want.Path]
		if !ok || f.Previous != want.Previous || f.Status != want.Status {
			return false
		}
		if !moved[want.Path] && (want.Previous == "" || !moved[want.Previous]) && f.Blob != want.Blob {
			return false
		}
	}
	return true
}
