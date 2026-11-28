package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// mainDrift lists the files baseBranch changed since its merge base with head
// (merge-queue mode). Any API doubt returns known=false, which the Main Drift
// rule treats as inconclusive.
func (c *GitHubClient) mainDrift(ctx context.Context, baseBranch, head string) ([]string, bool) {
	base, err := c.mergeBaseOf(ctx, baseBranch, head)
	if err != nil {
		return nil, false
	}
	return c.changedFiles(ctx, base, baseBranch)
}

func (c *GitHubClient) mergeBaseOf(ctx context.Context, baseBranch, head string) (string, error) {
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s?per_page=1", c.apiBase, c.owner, c.repo, url.PathEscape(baseBranch), url.PathEscape(head)))
	if err != nil {
		return "", err
	}
	var r struct {
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", err
	}
	if len(r.MergeBase.SHA) != 40 {
		return "", fmt.Errorf("no merge base")
	}
	return r.MergeBase.SHA, nil
}
