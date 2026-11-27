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
// Every other enqueuer, Green Lane's App included, must pass every rule.
var humanEnqueuerIDs = []int64{441017}

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
		HumanEnqueuerIDs: humanEnqueuerIDs, HoldLabels: holdLabels, DeniedPaths: config.DeniedPaths}
	n, ok := greenlane.QueueRefPR(mg.HeadRef)
	if !ok || !fullSHA(mg.BaseSHA) || !fullSHA(mg.HeadSHA) {
		return in, fmt.Errorf("unexpected merge_group payload (base_sha %q, head_sha %q, head_ref %q)", mg.BaseSHA, mg.HeadSHA, mg.HeadRef)
	}
	in.PRNumber = n

	q, err := c.queueEntry(ctx, n)
	if err != nil {
		return in, err
	}
	in.PRHead, in.EntryHead, in.EntryBase, in.Queue = q.head, q.entryHead, q.entryBase, q.config
	in.EnqueuerType, in.EnqueuerID, in.Labels = q.enqueuerType, q.enqueuerID, q.labels
	if in.GroupParent, err = c.parentsOf(ctx, mg.HeadSHA); err != nil {
		return in, err
	}
	if q.entryHead != mg.HeadSHA || q.entryBase != mg.BaseSHA {
		return in, nil // QueueGate rejects a group that isn't this entry's
	}
	// What the merge commit changes on the base: compared with the PR's own
	// file list after evaluation, so what lands is what was evaluated.
	in.LandedFiles, in.LandedFilesKnown = c.changedFiles(ctx, mg.BaseSHA, mg.HeadSHA)

	// Main Drift against the exact commit the PR lands on.
	mergeBase, err := c.mergeBaseOf(ctx, mg.BaseSHA, q.head)
	if err != nil {
		return in, err
	}
	in.LandingDrift, in.LandingDriftKnown = c.changedFiles(ctx, mergeBase, mg.BaseSHA)

	// Full evaluation of the PR head with the live worker's rules and the
	// same double read the evaluator uses to catch changes mid-collection.
	config.RequireAutomatedReview = true
	config.EnableMergeQueue()
	c.config = config
	first, err := c.GetPR(ctx, n)
	if err != nil {
		return in, err
	}
	pr, err := c.GetPR(ctx, n)
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
	in.Evaluation, in.PRFiles = &result, pr.Files
	return in, nil
}

type queuedPR struct {
	head, entryHead, entryBase string
	enqueuerType               string
	enqueuerID                 int64
	labels                     []string
	config                     greenlane.QueueConfig
}

func (c *GitHubClient) queueEntry(ctx context.Context, n int) (queuedPR, error) {
	const query = `query($owner:String!,$repo:String!,$n:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$n){number headRefOid isInMergeQueue
mergeQueueEntry{headCommit{oid} baseCommit{oid} enqueuer{__typename login ... on User{databaseId}}
 mergeQueue{configuration{mergeMethod mergingStrategy maximumEntriesToBuild}}}
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
					IsInMergeQueue bool   `json:"isInMergeQueue"`
					Entry          *struct {
						HeadCommit *oid `json:"headCommit"`
						BaseCommit *oid `json:"baseCommit"`
						Enqueuer   *struct {
							Type       string `json:"__typename"`
							DatabaseID int64  `json:"databaseId"`
						} `json:"enqueuer"`
						MergeQueue *struct {
							Configuration *struct {
								MergeMethod     string `json:"mergeMethod"`
								MergingStrategy string `json:"mergingStrategy"`
								MaxBuild        int    `json:"maximumEntriesToBuild"`
							} `json:"configuration"`
						} `json:"mergeQueue"`
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
	if !p.IsInMergeQueue || e == nil || e.HeadCommit == nil || e.BaseCommit == nil || e.Enqueuer == nil ||
		e.MergeQueue == nil || e.MergeQueue.Configuration == nil {
		return queuedPR{}, fmt.Errorf("PR #%d has no readable merge-queue entry", n)
	}
	cfg := e.MergeQueue.Configuration
	out := queuedPR{head: p.HeadRefOid, entryHead: e.HeadCommit.OID, entryBase: e.BaseCommit.OID,
		enqueuerType: e.Enqueuer.Type, enqueuerID: e.Enqueuer.DatabaseID,
		config: greenlane.QueueConfig{MergeMethod: cfg.MergeMethod, MergingStrategy: cfg.MergingStrategy, MaxEntriesBuild: cfg.MaxBuild}}
	for _, l := range p.Labels.Nodes {
		out.labels = append(out.labels, l.Name)
	}
	return out, nil
}

// changedFiles lists files changed between two commits (both paths of a
// rename). 300+ files may be truncated by GitHub and are reported unknown.
func (c *GitHubClient) changedFiles(ctx context.Context, from, to string) ([]string, bool) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", c.apiBase, c.owner, c.repo, url.PathEscape(from), url.PathEscape(to)))
	if err != nil {
		return nil, false
	}
	var r struct {
		Status string `json:"status"`
		Files  []struct {
			Filename string `json:"filename"`
			Previous string `json:"previous_filename"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &r) != nil || (r.Status != "ahead" && r.Status != "identical") || len(r.Files) >= 300 {
		return nil, false
	}
	var files []string
	for _, f := range r.Files {
		if strings.TrimSpace(f.Filename) == "" {
			return nil, false
		}
		files = append(files, f.Filename)
		if f.Previous != "" {
			files = append(files, f.Previous)
		}
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
