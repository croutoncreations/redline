package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/croutoncreations/redline/internal/greenlane"
)

// GitHub user IDs whose own hand-enqueue is the authorization (jfox85).
var humanEnqueuerIDs = []int64{441017}

// The Green Lane Merge App's bot login, in both forms GraphQL uses. Its
// enqueues must pass every rule; any other enqueuer is rejected.
var greenLaneBots = []string{"green-lane-merge", "green-lane-merge[bot]"}

var holdLabels = []string{"green-lane-hold", "maintenance:skip", "do-not-touch"}

// runQueueGate is the required merge_group check "green-lane-queue". It is
// built from the merge group's base (the workflow never checks out the
// merge group) and re-decides whether the queued PR may land.
func runQueueGate(ctx context.Context, c *GitHubClient, config greenlane.Config, eventPath string) error {
	if os.Getenv("GITHUB_EVENT_NAME") != "merge_group" {
		fmt.Println("green-lane-queue: not a merge-queue run; nothing to check")
		return nil
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		return fmt.Errorf("green-lane-queue: cannot read the event payload: %w", err)
	}
	in, err := c.queueGateInput(ctx, config, raw)
	if err != nil {
		return fmt.Errorf("green-lane-queue: %w", err)
	}
	if err := greenlane.QueueGate(in); err != nil {
		return fmt.Errorf("green-lane-queue: %w", err)
	}
	fmt.Printf("green-lane-queue: PR #%d at %s may merge onto %s\n", in.PRNumber, in.PRHead[:12], in.BaseSHA[:12])
	return nil
}

func (c *GitHubClient) queueGateInput(ctx context.Context, config greenlane.Config, raw []byte) (greenlane.QueueGateInput, error) {
	var payload struct {
		MergeGroup struct {
			BaseSHA string `json:"base_sha"`
			HeadSHA string `json:"head_sha"`
			HeadRef string `json:"head_ref"`
		} `json:"merge_group"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return greenlane.QueueGateInput{}, fmt.Errorf("unreadable merge_group payload: %w", err)
	}
	mg := payload.MergeGroup
	in := greenlane.QueueGateInput{BaseSHA: mg.BaseSHA, GroupHeadSHA: mg.HeadSHA, HeadRef: mg.HeadRef,
		HumanEnqueuerIDs: humanEnqueuerIDs, GreenLaneBots: greenLaneBots, HoldLabels: holdLabels, DeniedPaths: config.DeniedPaths}
	n, ok := greenlane.QueueRefPR(mg.HeadRef)
	if !ok || !fullSHA(mg.BaseSHA) || !fullSHA(mg.HeadSHA) {
		return in, fmt.Errorf("unexpected merge_group payload (base_sha %q, head_sha %q, head_ref %q)", mg.BaseSHA, mg.HeadSHA, mg.HeadRef)
	}
	in.PRNumber = n

	q, err := c.queueEntry(ctx, n)
	if err != nil {
		return in, err
	}
	in.PRHead, in.EntryHead, in.EntryBase = q.head, q.entryHead, q.entryBase
	// The queue's settings come from the branch rules (REST, readable with
	// contents: read); GraphQL's mergeQueue.configuration needs write access.
	if in.Queue, err = c.queueConfig(ctx, q.base); err != nil {
		return in, err
	}
	in.EnqueuerType, in.EnqueuerLogin, in.EnqueuerID, in.Labels = q.enqueuerType, q.enqueuerLogin, q.enqueuerID, q.labels
	if in.GroupParent, err = c.parentsOf(ctx, mg.HeadSHA); err != nil {
		return in, err
	}
	if q.entryHead != mg.HeadSHA || q.entryBase != mg.BaseSHA {
		return in, nil // QueueGate rejects a group that isn't this entry's
	}
	// What the merge commit changes on the base, file by file with blobs.
	landed, landedKnown := c.compareFiles(ctx, mg.BaseSHA, mg.HeadSHA)
	in.Landed, in.LandedFilesKnown = landed, landedKnown
	for _, f := range landed {
		in.LandedFiles = append(in.LandedFiles, f.Path)
		if f.Previous != "" {
			in.LandedFiles = append(in.LandedFiles, f.Previous)
		}
	}

	// Main Drift against the exact commit the PR lands on.
	mergeBase, err := c.mergeBaseOf(ctx, mg.BaseSHA, q.head)
	if err != nil {
		return in, err
	}
	in.LandingDrift, in.LandingDriftKnown = c.changedFiles(ctx, mergeBase, mg.BaseSHA)
	// The PR's own changes at its current head, with blobs, so the gate can
	// prove the merge commit lands exactly that content.
	in.PRChanges, in.PRChangesKnown = c.compareFiles(ctx, mergeBase, q.head)

	// Jon's own enqueue needs only the checks above; an API hiccup in the
	// full evaluation must not block it. Anyone else other than the App is
	// rejected by QueueGate without an evaluation.
	if in.HumanEnqueue() || !in.GreenLaneEnqueue() {
		return in, nil
	}

	// Full evaluation of the PR head with the live worker's rules and the
	// same double read the evaluator uses to catch changes mid-collection.
	config.RequireAutomatedReview = true
	config.EnableMergeQueue()
	c.config = config
	first, err := getPR(c, ctx, n)
	if err != nil {
		return in, err
	}
	pr, err := getPR(c, ctx, n)
	if err != nil {
		return in, err
	}
	if !sameEvidence(first, pr) {
		pr.SnapshotConsistent = false
		pr.SnapshotMessage = "metadata, files, checks, reviews, threads or receipts changed between reads"
	}
	if pr.HeadSHA != q.head {
		pr.SnapshotConsistent = false
		pr.SnapshotMessage = "PR head changed while the gate was collecting evidence"
	}
	// GitHub reports a queued PR as not cleanly mergeable. The queue built
	// the merge commit itself, and the gate binds to it above, so treat the
	// PR as mergeable rather than exempting the whole PR State rule.
	pr.Mergeable = true
	result := greenlane.NewEvaluator(config).Evaluate(pr, nil, nil)
	in.Evaluation = &result

	// Labels come from the queue read, before the evaluation. Re-read the
	// entry last so a hold label added meanwhile is seen, and confirm the
	// entry is still the one validated above.
	again, err := c.queueEntry(ctx, n)
	if err != nil {
		return in, err
	}
	if again.head != q.head || again.entryHead != q.entryHead || again.entryBase != q.entryBase ||
		again.enqueuerType != q.enqueuerType || again.enqueuerLogin != q.enqueuerLogin || again.enqueuerID != q.enqueuerID {
		return in, fmt.Errorf("PR #%d's queue entry changed while the gate was running", n)
	}
	in.Labels = again.labels
	return in, nil
}

// getPR is the PR evidence collector; a variable so tests can stub it.
var getPR = (*GitHubClient).GetPR

type queuedPR struct {
	head, base, entryHead, entryBase string
	enqueuerType, enqueuerLogin      string
	enqueuerID                       int64
	labels                           []string
}

func (c *GitHubClient) queueEntry(ctx context.Context, n int) (queuedPR, error) {
	const query = `query($owner:String!,$repo:String!,$n:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$n){number headRefOid isInMergeQueue
baseRefName mergeQueueEntry{headCommit{oid} baseCommit{oid} enqueuer{__typename login ... on User{databaseId}}}
labels(first:100){totalCount nodes{name}}}}}`
	body, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"owner": c.owner, "repo": c.repo, "n": n}})
	if err != nil {
		return queuedPR{}, err
	}
	data, err := c.request(ctx, "POST", c.apiBase+"/graphql", body)
	if err != nil {
		return queuedPR{}, err
	}
	type oid struct {
		OID string `json:"oid"`
	}
	var r struct {
		Errors []any `json:"errors"`
		Data   struct {
			Repository struct {
				PullRequest *struct {
					Number         int    `json:"number"`
					HeadRefOid     string `json:"headRefOid"`
					BaseRefName    string `json:"baseRefName"`
					IsInMergeQueue bool   `json:"isInMergeQueue"`
					Entry          *struct {
						HeadCommit *oid `json:"headCommit"`
						BaseCommit *oid `json:"baseCommit"`
						Enqueuer   *struct {
							Type       string `json:"__typename"`
							Login      string `json:"login"`
							DatabaseID int64  `json:"databaseId"`
						} `json:"enqueuer"`
					} `json:"mergeQueueEntry"`
					Labels struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"labels"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return queuedPR{}, err
	}
	p := r.Data.Repository.PullRequest
	if len(r.Errors) > 0 || p == nil || p.Number != n || !fullSHA(p.HeadRefOid) || p.Labels.TotalCount != len(p.Labels.Nodes) {
		return queuedPR{}, fmt.Errorf("incomplete merge-queue data for PR #%d", n)
	}
	e := p.Entry
	if !p.IsInMergeQueue || e == nil || e.HeadCommit == nil || e.BaseCommit == nil || e.Enqueuer == nil || p.BaseRefName == "" {
		return queuedPR{}, fmt.Errorf("PR #%d has no readable merge-queue entry", n)
	}
	out := queuedPR{head: p.HeadRefOid, base: p.BaseRefName, entryHead: e.HeadCommit.OID, entryBase: e.BaseCommit.OID,
		enqueuerType: e.Enqueuer.Type, enqueuerLogin: e.Enqueuer.Login, enqueuerID: e.Enqueuer.DatabaseID}
	for _, l := range p.Labels.Nodes {
		out.labels = append(out.labels, l.Name)
	}
	return out, nil
}

// queueConfig reads the merge-queue rule that applies to branch. More than
// one merge_queue rule (several rulesets) is treated as unreadable.
func (c *GitHubClient) queueConfig(ctx context.Context, branch string) (greenlane.QueueConfig, error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/rules/branches/%s?per_page=100", c.apiBase, c.owner, c.repo, url.PathEscape(branch)))
	if err != nil {
		return greenlane.QueueConfig{}, err
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			MergeMethod      string `json:"merge_method"`
			GroupingStrategy string `json:"grouping_strategy"`
			MaxBuild         int    `json:"max_entries_to_build"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		return greenlane.QueueConfig{}, err
	}
	var found []greenlane.QueueConfig
	for _, r := range rules {
		if r.Type == "merge_queue" {
			found = append(found, greenlane.QueueConfig{MergeMethod: r.Parameters.MergeMethod, MergingStrategy: r.Parameters.GroupingStrategy, MaxEntriesBuild: r.Parameters.MaxBuild})
		}
	}
	if len(found) != 1 {
		return greenlane.QueueConfig{}, fmt.Errorf("expected exactly one merge_queue rule on %s, found %d", branch, len(found))
	}
	return found[0], nil
}

// changedFiles lists files changed between two commits (both paths of a
// rename). 300+ files may be truncated by GitHub and are reported unknown.
func (c *GitHubClient) changedFiles(ctx context.Context, from, to string) ([]string, bool) {
	files, ok := c.compareFiles(ctx, from, to)
	if !ok {
		return nil, false
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
		if f.Previous != "" {
			out = append(out, f.Previous)
		}
	}
	return out, true
}

// compareFiles returns each changed file with its status and resulting blob.
func (c *GitHubClient) compareFiles(ctx context.Context, from, to string) ([]greenlane.FileChange, bool) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", c.apiBase, c.owner, c.repo, url.PathEscape(from), url.PathEscape(to)))
	if err != nil {
		return nil, false
	}
	var r struct {
		Status string `json:"status"`
		Files  []struct {
			Filename string `json:"filename"`
			Previous string `json:"previous_filename"`
			Status   string `json:"status"`
			SHA      string `json:"sha"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &r) != nil || (r.Status != "ahead" && r.Status != "identical") || len(r.Files) >= 300 {
		return nil, false
	}
	var files []greenlane.FileChange
	for _, f := range r.Files {
		if strings.TrimSpace(f.Filename) == "" || f.Status == "" {
			return nil, false
		}
		files = append(files, greenlane.FileChange{Path: f.Filename, Previous: f.Previous, Status: f.Status, Blob: f.SHA})
	}
	return files, true
}

func (c *GitHubClient) parentsOf(ctx context.Context, sha string) ([]string, error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/commits/%s", c.apiBase, c.owner, c.repo, url.PathEscape(sha)))
	if err != nil {
		return nil, err
	}
	var r struct {
		SHA     string `json:"sha"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.SHA != sha {
		return nil, fmt.Errorf("commit %s not found", sha)
	}
	var out []string
	for _, p := range r.Parents {
		out = append(out, p.SHA)
	}
	return out, nil
}

func fullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
