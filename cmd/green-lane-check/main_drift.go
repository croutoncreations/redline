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
	// compare/<mergeBase>...<baseBranch> lists what main changed since then.
	// GitHub returns at most 300 files for a comparison, so a list that long
	// may be truncated and is reported as unknown.
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/compare/%s...%s", c.apiBase, c.owner, c.repo, url.PathEscape(base), url.PathEscape(baseBranch)))
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
		if f.Filename == "" {
			return nil, false
		}
		files = append(files, f.Filename)
		if f.Previous != "" {
			files = append(files, f.Previous)
		}
	}
	return files, true
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
