package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/greenlane"
)

type GitHubClient struct {
	token, owner, repo, apiBase string
	client                      *http.Client
	config                      greenlane.Config
}

type prMetadata struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	State        string `json:"state"`
	UpdatedAt    string `json:"updated_at"`
	Draft        bool   `json:"draft"`
	ChangedFiles int    `json:"changed_files"`
	Head         struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
	Mergeable *bool `json:"mergeable"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c *GitHubClient) metadata(ctx context.Context, n int) (prMetadata, error) {
	var p prMetadata
	data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.apiBase, c.owner, c.repo, n))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(data, &p)
	if err == nil && (p.Number != n || p.Head.SHA == "" || p.Base.SHA == "" || p.Base.Ref == "" || p.User.Login == "" || (p.State != "open" && p.State != "closed")) {
		err = fmt.Errorf("incomplete PR metadata")
	}
	return p, err
}

func (c *GitHubClient) GetPR(ctx context.Context, n int) (greenlane.PRInfo, error) {
	var pr greenlane.PRInfo
	before, err := c.metadata(ctx, n)
	if err != nil {
		return pr, err
	}
	files, err := c.files(ctx, n, before.ChangedFiles)
	if err != nil {
		return pr, err
	}
	var checkEvidence []greenlane.CheckEvidence
	checks, receipts, provenanceError, err := c.checks(ctx, before.Head.SHA, &checkEvidence)
	if err != nil {
		return pr, err
	}
	reviews, err := c.reviews(ctx, n)
	if err != nil {
		return pr, err
	}
	comments, err := c.reviewComments(ctx, n)
	if err != nil {
		return pr, err
	}
	automated, err := c.automatedReviews(ctx, n)
	if err != nil {
		return pr, err
	}
	threads, err := c.threads(ctx, n, before.Head.SHA, before.Base.SHA)
	if err != nil {
		return pr, err
	}
	after, err := c.metadata(ctx, n)
	if err != nil {
		return pr, err
	}
	pr = greenlane.PRInfo{
		CheckEvidence: checkEvidence, AutomatedReviews: automated,
		Repository: c.owner + "/" + c.repo, Number: before.Number, Title: before.Title, Body: before.Body,
		State: before.State, UpdatedAt: before.UpdatedAt, HeadSHA: before.Head.SHA, BaseSHA: before.Base.SHA,
		BaseBranch: before.Base.Ref, Draft: before.Draft, Mergeable: before.Mergeable != nil && *before.Mergeable,
		Author: before.User.Login, Files: files, CheckRuns: checks, Reviews: reviews, ReviewComments: comments,
		ReviewThreads: threads, ReviewThreadsComplete: true, Provenance: receipts, ProvenanceError: provenanceError,
		SnapshotConsistent: reflect.DeepEqual(before, after),
	}
	if !pr.SnapshotConsistent {
		pr.SnapshotMessage = "PR metadata changed while fetching evidence"
	}
	return pr, nil
}

// The file count catches GitHub's 3,000-file cap and incomplete responses.
func (c *GitHubClient) files(ctx context.Context, n, expected int) ([]greenlane.PRFile, error) {
	var all []greenlane.PRFile
	for page := 1; page <= 30; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", c.apiBase, c.owner, c.repo, n, page))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Filename  string `json:"filename"`
			Previous  string `json:"previous_filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Changes   int    `json:"changes"`
			Patch     string `json:"patch"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		for _, f := range rows {
			all = append(all, greenlane.PRFile{Filename: f.Filename, PreviousFilename: f.Previous, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Changes: f.Changes, Patch: f.Patch})
		}
		if len(rows) < 100 {
			if expected >= 0 && len(all) != expected {
				return nil, fmt.Errorf("incomplete PR file list: got %d, expected %d", len(all), expected)
			}
			return all, nil
		}
	}
	return nil, fmt.Errorf("PR file pagination reached API cap")
}

func (c *GitHubClient) checks(ctx context.Context, head string, evidence *[]greenlane.CheckEvidence) ([]greenlane.CheckRun, []greenlane.SignedReceipt, string, error) {
	var all []greenlane.CheckRun
	var receipts []greenlane.SignedReceipt
	var evidenceError string
	workflows := map[int64]int64{}
	fetched := 0
	for page := 1; page <= 100; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs?per_page=100&page=%d", c.apiBase, c.owner, c.repo, head, page))
		if err != nil {
			return nil, nil, "", err
		}
		var response struct {
			Total int `json:"total_count"`
			Runs  []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				Head       string `json:"head_sha"`
				Details    string `json:"details_url"`
				App        struct {
					ID int64 `json:"id"`
				} `json:"app"`
				Output struct {
					Text string `json:"text"`
				} `json:"output"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, nil, "", err
		}
		fetched += len(response.Runs)
		for _, row := range response.Runs {
			if row.Name == "green-lane-provenance" {
				trustedApp := false
				for _, issuer := range c.config.ProvenanceIssuers {
					if issuer.AppID > 0 && issuer.AppID == row.App.ID {
						trustedApp = true
					}
				}
				if !trustedApp {
					continue
				}
				*evidence = append(*evidence, greenlane.NewCheckEvidence(row.ID, row.Name, row.Status, row.Conclusion, row.Head, row.App.ID, row.Details, row.Output.Text))
				if row.Head != head || row.Status != "completed" || row.Conclusion != "success" {
					evidenceError = "provenance publishing check is stale or incomplete"
					continue
				}
				var bundle []greenlane.SignedReceipt
				if len(row.Output.Text) > 65535 || json.Unmarshal([]byte(row.Output.Text), &bundle) != nil || len(bundle) == 0 || len(bundle) > 100 {
					evidenceError = "invalid provenance check payload"
					continue
				}
				for _, r := range bundle {
					r.SourceAppID = row.App.ID
					receipts = append(receipts, r)
				}
				continue
			}
			source, required := c.config.CheckSources[row.Name]
			if !required {
				continue
			}
			*evidence = append(*evidence, greenlane.NewCheckEvidence(row.ID, row.Name, row.Status, row.Conclusion, row.Head, row.App.ID, row.Details, ""))
			check := greenlane.CheckRun{Name: row.Name, Status: row.Status, Conclusion: row.Conclusion, AppID: row.App.ID, HeadSHA: row.Head}
			if source.AppID == row.App.ID {
				// details_url is data, never a fetch target. Resolve only numeric run
				// IDs in this repo via our fixed authenticated API origin.
				u, err := url.Parse(row.Details)
				prefix := "/" + c.owner + "/" + c.repo + "/actions/runs/"
				if err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && strings.HasPrefix(u.Path, prefix) {
					parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
					if len(parts) != 3 || parts[1] != "job" {
						all = append(all, check)
						continue
					}
					runID, err := strconv.ParseInt(parts[0], 10, 64)
					jobID, jobErr := strconv.ParseInt(parts[2], 10, 64)
					if err == nil && jobErr == nil && runID > 0 && jobID > 0 {
						rawJob, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d", c.apiBase, c.owner, c.repo, jobID))
						if err != nil {
							return nil, nil, "", err
						}
						var job struct {
							RunID      int64  `json:"run_id"`
							CheckURL   string `json:"check_run_url"`
							Head       string `json:"head_sha"`
							Name       string `json:"name"`
							Status     string `json:"status"`
							Conclusion string `json:"conclusion"`
						}
						if err := json.Unmarshal(rawJob, &job); err != nil {
							return nil, nil, "", err
						}
						expectedURL := fmt.Sprintf("%s/repos/%s/%s/check-runs/%d", c.apiBase, c.owner, c.repo, row.ID)
						if row.ID <= 0 || job.RunID != runID || job.CheckURL != expectedURL || job.Head != head || job.Name != row.Name || job.Status != row.Status || job.Conclusion != row.Conclusion {
							all = append(all, check)
							continue
						}
						workflow, ok := workflows[runID]
						if !ok {
							raw, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d", c.apiBase, c.owner, c.repo, runID))
							if err != nil {
								return nil, nil, "", err
							}
							var run struct {
								WorkflowID int64  `json:"workflow_id"`
								Head       string `json:"head_sha"`
							}
							if err := json.Unmarshal(raw, &run); err != nil {
								return nil, nil, "", err
							}
							if run.Head == head {
								workflow = run.WorkflowID
							}
							workflows[runID] = workflow
						}
						check.WorkflowID = workflow
					}
				}
			}
			all = append(all, check)
		}
		if len(response.Runs) < 100 {
			if fetched != response.Total {
				return nil, nil, "", fmt.Errorf("incomplete check-run pagination")
			}
			return all, receipts, evidenceError, nil
		}
	}
	return nil, nil, "", fmt.Errorf("check-run pagination did not finish")
}

func (c *GitHubClient) reviews(ctx context.Context, n int) ([]greenlane.Review, error) {
	var all []greenlane.Review
	for page := 1; page <= 100; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100&page=%d", c.apiBase, c.owner, c.repo, n, page))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID        int64     `json:"id"`
			State     string    `json:"state"`
			Commit    string    `json:"commit_id"`
			Submitted time.Time `json:"submitted_at"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			all = append(all, greenlane.Review{ID: r.ID, User: r.User.Login, State: r.State, CommitID: r.Commit, SubmittedAt: r.Submitted})
		}
		if len(rows) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("review pagination did not finish")
}

// GraphQL exposes resolution; REST's flat review-comment list does not.
func (c *GitHubClient) threads(ctx context.Context, n int, head, base string) ([]greenlane.ReviewThread, error) {
	const query = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){headRefOid baseRefOid reviewThreads(first:100,after:$cursor){totalCount nodes{id isResolved isOutdated} pageInfo{hasNextPage endCursor}}}}}`
	var all []greenlane.ReviewThread
	var cursor *string
	seen := map[string]bool{}
	cursors := map[string]bool{}
	for page := 0; page < 100; page++ {
		payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"owner": c.owner, "name": c.repo, "number": n, "cursor": cursor}})
		data, err := c.request(ctx, "POST", c.apiBase+"/graphql", payload)
		if err != nil {
			return nil, err
		}
		var response struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Repository *struct {
					PR *struct {
						Head    string `json:"headRefOid"`
						Base    string `json:"baseRefOid"`
						Threads *struct {
							Total int                      `json:"totalCount"`
							Nodes []greenlane.ReviewThread `json:"nodes"`
							Page  *struct {
								Next   bool    `json:"hasNextPage"`
								Cursor *string `json:"endCursor"`
							} `json:"pageInfo"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, err
		}
		if len(response.Errors) > 0 || response.Data.Repository == nil || response.Data.Repository.PR == nil {
			return nil, fmt.Errorf("review-thread query failed or returned partial data")
		}
		pr := response.Data.Repository.PR
		if pr.Head != head || pr.Base != base || pr.Threads == nil || pr.Threads.Page == nil {
			return nil, fmt.Errorf("review-thread snapshot changed or is incomplete")
		}
		for _, thread := range pr.Threads.Nodes {
			if thread.ID == "" || seen[thread.ID] {
				return nil, fmt.Errorf("invalid or duplicate review thread")
			}
			seen[thread.ID] = true
			all = append(all, thread)
		}
		if !pr.Threads.Page.Next {
			if len(all) != pr.Threads.Total {
				return nil, fmt.Errorf("incomplete review-thread pagination")
			}
			return all, nil
		}
		next := pr.Threads.Page.Cursor
		if next == nil || *next == "" || cursors[*next] {
			return nil, fmt.Errorf("review-thread pagination made no progress")
		}
		cursors[*next] = true
		cursor = next
	}
	return nil, fmt.Errorf("review-thread pagination did not finish")
}

func (c *GitHubClient) GetOpenPRs(ctx context.Context, exclude ...int) ([]greenlane.PRSummary, error) {
	var all []greenlane.PRSummary
	for page := 1; page <= 100; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=100&page=%d", c.apiBase, c.owner, c.repo, page))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			Number int    `json:"number"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			if len(exclude) > 0 && row.Number == exclude[0] {
				continue
			}
			if row.Number <= 0 {
				return nil, fmt.Errorf("invalid open PR number")
			}
			meta, err := c.metadata(ctx, row.Number)
			if err != nil {
				return nil, err
			}
			files, err := c.files(ctx, row.Number, meta.ChangedFiles)
			if err != nil {
				return nil, err
			}
			summary := greenlane.PRSummary{Number: row.Number}
			for _, line := range strings.Split(meta.Body, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "Root-Cause:") {
					summary.RootCause = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Root-Cause:"))
					break
				}
			}
			for _, file := range files {
				summary.Files = append(summary.Files, file.Paths()...)
			}
			after, err := c.metadata(ctx, row.Number)
			if err != nil {
				return nil, err
			}
			if meta.Number != after.Number || meta.Body != after.Body || meta.State != after.State || meta.ChangedFiles != after.ChangedFiles || meta.Head != after.Head || meta.Base != after.Base {
				return nil, fmt.Errorf("duplicate PR snapshot changed")
			}
			all = append(all, summary)
		}
		if len(rows) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("open-PR pagination did not finish")
}

// The reporter's own check is excluded during collection, so its progress
// cannot invalidate the evidence snapshot. Sort API lists before comparing.
func sameEvidence(a, b greenlane.PRInfo) bool {
	normalize := func(p greenlane.PRInfo) greenlane.PRInfo {
		p.AutomatedReviews = append([]greenlane.AutomatedReviewComment{}, p.AutomatedReviews...)
		sort.Slice(p.AutomatedReviews, func(i, j int) bool { return p.AutomatedReviews[i].ID < p.AutomatedReviews[j].ID })
		p.Files = append([]greenlane.PRFile(nil), p.Files...)
		sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Filename < p.Files[j].Filename })
		p.Reviews = append([]greenlane.Review(nil), p.Reviews...)
		sort.Slice(p.Reviews, func(i, j int) bool { return p.Reviews[i].ID < p.Reviews[j].ID })
		p.ReviewThreads = append([]greenlane.ReviewThread(nil), p.ReviewThreads...)
		sort.Slice(p.ReviewThreads, func(i, j int) bool { return p.ReviewThreads[i].ID < p.ReviewThreads[j].ID })
		p.ReviewComments = append([]greenlane.ReviewComment(nil), p.ReviewComments...)
		sort.Slice(p.ReviewComments, func(i, j int) bool { return p.ReviewComments[i].ID < p.ReviewComments[j].ID })
		p.Provenance = append([]greenlane.SignedReceipt(nil), p.Provenance...)
		sort.Slice(p.Provenance, func(i, j int) bool {
			return p.Provenance[i].KeyID+p.Provenance[i].Signature < p.Provenance[j].KeyID+p.Provenance[j].Signature
		})
		p.CheckEvidence = append([]greenlane.CheckEvidence{}, p.CheckEvidence...)
		sort.Slice(p.CheckEvidence, func(i, j int) bool { return p.CheckEvidence[i].ID < p.CheckEvidence[j].ID })
		p.CheckRuns = append([]greenlane.CheckRun(nil), p.CheckRuns...)
		sort.Slice(p.CheckRuns, func(i, j int) bool { return fmt.Sprint(p.CheckRuns[i]) < fmt.Sprint(p.CheckRuns[j]) })
		return p
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}
func (c *GitHubClient) doRequest(ctx context.Context, method, u string) ([]byte, error) {
	return c.request(ctx, method, u, nil)
}
func (c *GitHubClient) request(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub API error %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

func (c *GitHubClient) reviewComments(ctx context.Context, n int) ([]greenlane.ReviewComment, error) {
	var all []greenlane.ReviewComment
	for page := 1; page <= 100; page++ {
		data, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments?per_page=100&page=%d", c.apiBase, c.owner, c.repo, n, page))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID       int64  `json:"id"`
			ReviewID int64  `json:"pull_request_review_id"`
			Path     string `json:"path"`
			Line     int    `json:"line"`
			Body     string `json:"body"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			all = append(all, greenlane.ReviewComment{ID: r.ID, ReviewID: r.ReviewID, Path: r.Path, Line: r.Line, Body: r.Body})
		}
		if len(rows) < 100 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("review-comment pagination did not finish")
}

// Fetch every page: losing an updated or deleted summary must not leave a
// stale completion authoritative. Untrusted comments are never evidence.
func (c *GitHubClient) automatedReviews(ctx context.Context, n int) ([]greenlane.AutomatedReviewComment, error) {
	all := []greenlane.AutomatedReviewComment{}
	seen := map[int64]bool{}
	nodeIDs := []string{}
	for page := 1; page <= 100; page++ {
		raw, err := c.doRequest(ctx, "GET", fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", c.apiBase, c.owner, c.repo, n, page))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID        int64  `json:"id"`
			NodeID    string `json:"node_id"`
			Body      string `json:"body"`
			UpdatedAt string `json:"updated_at"`
			User      struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
			App *struct {
				ID int64 `json:"id"`
			} `json:"performed_via_github_app"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.ID <= 0 || seen[r.ID] {
				return nil, fmt.Errorf("invalid or repeated issue-comment page")
			}
			seen[r.ID] = true
			if r.App == nil {
				continue
			}
			item := greenlane.AutomatedReviewComment{ID: r.ID, Body: r.Body, UpdatedAt: r.UpdatedAt, UserID: r.User.ID, User: r.User.Login, UserType: r.User.Type, AppID: r.App.ID}
			if greenlane.TrustedAutomatedSummary(item) {
				if r.NodeID == "" {
					return nil, fmt.Errorf("missing summary node ID")
				}
				nodeIDs = append(nodeIDs, r.NodeID)
				all = append(all, item)
			}
		}
		if len(rows) < 100 {
			return c.summaryEditors(ctx, all, nodeIDs)
		}
	}
	return nil, fmt.Errorf("issue-comment pagination did not finish")
}

// Join REST App attribution to GraphQL editor identity over the same raw body.
func (c *GitHubClient) summaryEditors(ctx context.Context, all []greenlane.AutomatedReviewComment, ids []string) ([]greenlane.AutomatedReviewComment, error) {
	const query = `query($ids:[ID!]!){nodes(ids:$ids){... on IssueComment{id databaseId body updatedAt lastEditedAt editor{__typename login ... on Bot{databaseId}}}}}`
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		payload, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"ids": ids[start:end]}})
		data, err := c.request(ctx, "POST", c.apiBase+"/graphql", payload)
		if err != nil {
			return nil, err
		}
		var response struct {
			Errors []json.RawMessage `json:"errors"`
			Data   struct {
				Nodes []*struct {
					ID           string `json:"id"`
					DatabaseID   int64  `json:"databaseId"`
					Body         string `json:"body"`
					UpdatedAt    string `json:"updatedAt"`
					LastEditedAt string `json:"lastEditedAt"`
					Editor       *struct {
						Type  string `json:"__typename"`
						Login string `json:"login"`
						ID    int64  `json:"databaseId"`
					} `json:"editor"`
				} `json:"nodes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return nil, err
		}
		if len(response.Errors) > 0 || len(response.Data.Nodes) != end-start {
			return nil, fmt.Errorf("summary editor query incomplete")
		}
		for i, node := range response.Data.Nodes {
			item := &all[start+i]
			if node == nil || node.ID != ids[start+i] || node.DatabaseID != item.ID || node.Body != item.Body || node.UpdatedAt != item.UpdatedAt {
				return nil, fmt.Errorf("summary changed during editor verification")
			}
			item.LastEditedAt = node.LastEditedAt
			if node.Editor != nil {
				item.EditorID = node.Editor.ID
				item.EditorUser = node.Editor.Login
				item.EditorType = node.Editor.Type
			}
		}
	}
	return all, nil
}
