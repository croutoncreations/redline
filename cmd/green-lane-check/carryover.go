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
// summaries on this PR, whose change is byte-identical to head's (see
// internal/greenlane/review_carryover.go). Each must be an ancestor of head,
// and each change is measured against its merge base with the base branch.
// Any API doubt is an error, so the evaluation fails closed.
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
		base, err := c.mergeBase(ctx, baseBranch, sha)
		if err != nil {
			return nil, err
		}
		reviewedTree, err := tree(sha)
		if err != nil {
			return nil, err
		}
		reviewedBaseTree, err := tree(base)
		if err != nil {
			return nil, err
		}
		if greenlane.SameChange(headTree, headBaseTree, reviewedTree, reviewedBaseTree) {
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
