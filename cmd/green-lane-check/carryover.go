package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/croutoncreations/redline/internal/greenlane"
)

// reviewEquivalentHeads returns the earlier heads, named by trusted review
// summaries on this PR, whose change is path-by-path identical to head's (see
// internal/greenlane/review_carryover.go). Each must be an ancestor of head.
//
// The reviewed change is measured against the reviewed commit's FIRST-PARENT
// base: walking first parents from the reviewed commit back to the first
// commit that is also on the base branch. That is the base the reviewer saw,
// and it does not move when main later absorbs PR commits (a merge base
// computed against today's main would). Only plain PR histories qualify:
// every commit on that walk must have one parent, apart from merges of the
// base branch itself, so a branch built by merging other branches gets no
// carryover. Any API doubt is an error, so the caller treats it as no
// carryover.
func (c *GitHubClient) reviewEquivalentHeads(ctx context.Context, baseBranch, head string, summaries []greenlane.AutomatedReviewComment) ([]string, error) {
	candidates := map[string]bool{}
	for _, s := range summaries {
		for _, sha := range greenlane.ReviewedCommits(s) {
			if sha != head && fullSHA(sha) {
				candidates[sha] = true
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	headBase, err := c.mergeBase(ctx, baseBranch, head)
	if err != nil {
		return nil, err
	}
	trees := map[string]greenlane.Tree{}
	tree := func(sha string) (greenlane.Tree, error) {
		if t, ok := trees[sha]; ok {
			return t, nil
		}
		t, err := c.tree(ctx, sha)
		if err == nil {
			trees[sha] = t
		}
		return t, err
	}
	headTree, err := tree(head)
	if err != nil {
		return nil, err
	}
	headBaseTree, err := tree(headBase)
	if err != nil {
		return nil, err
	}
	var out []string
	for sha := range candidates {
		// A summary could name any commit; only this PR's own history counts.
		ancestor, err := c.isAncestor(ctx, sha, head)
		if err != nil {
			return nil, err
		}
		if !ancestor {
			continue
		}
		base, ok, err := c.reviewedBase(ctx, baseBranch, sha)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		reviewedTree, err := tree(sha)
		if err != nil {
			return nil, err
		}
		reviewedBaseTree, err := tree(base)
		if err != nil {
			return nil, err
		}
		if greenlane.SameChange(headTree, headBaseTree, reviewedTree, reviewedBaseTree) &&
			greenlane.BaseDriftAllowed(headTree, headBaseTree, reviewedBaseTree, c.config.DeniedPaths) {
			out = append(out, sha)
		}
	}
	sort.Strings(out)
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

func (c *GitHubClient) compare(ctx context.Context, base, head string) (status, mergeBase string, err error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s?per_page=1", c.apiBase, c.owner, c.repo, url.PathEscape(base), url.PathEscape(head)))
	if err != nil {
		return "", "", err
	}
	var r struct {
		Status    string `json:"status"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", "", err
	}
	if r.Status == "" || !fullSHA(r.MergeBase.SHA) {
		return "", "", fmt.Errorf("incomplete compare %s...%s", base, head)
	}
	return r.Status, r.MergeBase.SHA, nil
}

func (c *GitHubClient) mergeBase(ctx context.Context, baseBranch, sha string) (string, error) {
	_, base, err := c.compare(ctx, baseBranch, sha)
	return base, err
}

func (c *GitHubClient) isAncestor(ctx context.Context, ancestor, head string) (bool, error) {
	status, base, err := c.compare(ctx, ancestor, head)
	if err != nil {
		return false, err
	}
	return status == "ahead" && base == ancestor, nil
}

// reviewedBase walks first parents from a reviewed PR commit to the first
// commit already on baseBranch. A merge commit on the walk must have that
// base-branch commit as its second parent (a "merge main into the PR"); any
// other merge, or a walk longer than maxWalk commits, gives no base.
func (c *GitHubClient) reviewedBase(ctx context.Context, baseBranch, sha string) (string, bool, error) {
	const maxWalk = 100
	current := sha
	for i := 0; i < maxWalk; i++ {
		onBase, err := c.isAncestorOrSame(ctx, current, baseBranch)
		if err != nil {
			return "", false, err
		}
		if onBase {
			if current == sha {
				return "", false, nil // the "reviewed" commit is main itself
			}
			return c.linearBase(ctx, baseBranch, current)
		}
		parents, err := c.parents(ctx, current)
		if err != nil {
			return "", false, err
		}
		switch len(parents) {
		case 1:
			current = parents[0]
		case 2:
			// Allow only merges that bring the base branch into the PR.
			onBase, err := c.isAncestorOrSame(ctx, parents[1], baseBranch)
			if err != nil {
				return "", false, err
			}
			if !onBase {
				return "", false, nil
			}
			// The change the reviewer saw is relative to that merged base.
			return c.linearBase(ctx, baseBranch, parents[1])
		default:
			return "", false, nil
		}
	}
	return "", false, nil
}

// linearBase accepts base only if every base-branch commit after it has a
// single parent. A merge commit there could have brought one of this PR's own
// commits into the base branch, which would make the walk stop early and
// shrink the change the review is compared against.
func (c *GitHubClient) linearBase(ctx context.Context, baseBranch, base string) (string, bool, error) {
	const perPage, maxPages = 100, 10
	seen := 0
	for page := 1; page <= maxPages; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s?per_page=%d&page=%d", c.apiBase, c.owner, c.repo, url.PathEscape(base), url.PathEscape(baseBranch), perPage, page))
		if err != nil {
			return "", false, err
		}
		var r struct {
			Status  string `json:"status"`
			Total   int    `json:"total_commits"`
			Commits []struct {
				Parents []struct {
					SHA string `json:"sha"`
				} `json:"parents"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", false, err
		}
		if r.Status != "ahead" && r.Status != "identical" {
			return "", false, nil
		}
		for _, commit := range r.Commits {
			if len(commit.Parents) != 1 {
				return "", false, nil
			}
		}
		seen += len(r.Commits)
		if seen >= r.Total {
			return base, true, nil
		}
		if len(r.Commits) == 0 {
			return "", false, fmt.Errorf("compare %s...%s pagination stalled", base, baseBranch)
		}
	}
	return "", false, nil
}

func (c *GitHubClient) isAncestorOrSame(ctx context.Context, ancestor, head string) (bool, error) {
	status, base, err := c.compare(ctx, ancestor, head)
	if err != nil {
		return false, err
	}
	return (status == "ahead" || status == "identical") && base == ancestor, nil
}

func (c *GitHubClient) parents(ctx context.Context, sha string) ([]string, error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/git/commits/%s", c.apiBase, c.owner, c.repo, sha))
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
	if r.SHA != sha || len(r.Parents) == 0 {
		return nil, fmt.Errorf("incomplete commit %s", sha)
	}
	out := make([]string, len(r.Parents))
	for i, p := range r.Parents {
		if !fullSHA(p.SHA) {
			return nil, fmt.Errorf("invalid parent of %s", sha)
		}
		out[i] = p.SHA
	}
	return out, nil
}

// tree reads a full recursive git tree. Truncation fails closed.
func (c *GitHubClient) tree(ctx context.Context, sha string) (greenlane.Tree, error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", c.apiBase, c.owner, c.repo, sha))
	if err != nil {
		return nil, err
	}
	var r struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Truncated || r.SHA == "" || len(r.Tree) == 0 {
		return nil, fmt.Errorf("incomplete git tree for %s", sha)
	}
	t := greenlane.Tree{}
	for _, e := range r.Tree {
		if e.Type == "tree" {
			continue
		}
		if e.Path == "" || e.SHA == "" || e.Mode == "" {
			return nil, fmt.Errorf("incomplete git tree entry in %s", sha)
		}
		if _, dup := t[e.Path]; dup {
			return nil, fmt.Errorf("duplicate git tree path in %s", sha)
		}
		t[e.Path] = greenlane.TreeEntry{Mode: e.Mode, Type: e.Type, SHA: e.SHA}
	}
	return t, nil
}
