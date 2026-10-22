package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/greenlane"
)

type apiFixture struct {
	mode          string
	metadataReads int
	threadPages   int
	runReads      int
}

func apiClient(t *testing.T, mode string) (*GitHubClient, *apiFixture) {
	t.Helper()
	f := &apiFixture{mode: mode}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		send := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch {
		case r.URL.Path == "/graphql":
			f.threadPages++
			var req struct {
				Variables struct {
					Cursor *string `json:"cursor"`
				} `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if mode == "graphql partial error" {
				send(map[string]any{"errors": []any{map[string]string{"message": "denied"}}, "data": map[string]any{}})
				return
			}
			if mode == "graphql null" {
				send(map[string]any{"data": map[string]any{"repository": nil}})
				return
			}
			head := "head"
			if mode == "graphql stale" {
				head = "old"
			}
			nodes := []any{map[string]any{"id": "thread-1", "isResolved": true, "isOutdated": true}}
			total := 1
			next := false
			cursor := any(nil)
			if mode == "two thread pages" || mode == "missing cursor" || mode == "repeated cursor" {
				total = 2
				next = req.Variables.Cursor == nil
				cursor = "cursor-1"
				if !next {
					nodes = []any{map[string]any{"id": "thread-2", "isResolved": true}}
				}
			}
			if mode == "missing cursor" {
				cursor = nil
			}
			if mode == "repeated cursor" {
				nodes = []any{}
				next = true
			}
			if mode == "thread count mismatch" {
				total = 2
			}
			send(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": head, "baseRefOid": "base", "reviewThreads": map[string]any{"totalCount": total, "nodes": nodes, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": cursor}}}}}})
		case r.URL.Path == "/repos/owner/repo/pulls":
			send([]any{map[string]any{"number": 1, "body": "stale root cause"}})
		case strings.HasSuffix(r.URL.Path, "/files"):
			name := "README.md"
			previous := ""
			if mode == "renamed duplicate" {
				name = "src/new.go"
				previous = "src/old.go"
			}
			if mode == "missing file page" {
				send([]any{})
				return
			}
			send([]any{map[string]any{"filename": name, "previous_filename": previous, "status": "modified", "additions": 1}})
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			if mode == "review page error" {
				w.WriteHeader(403)
				return
			}
			send([]any{map[string]any{"id": 1, "state": "APPROVED", "commit_id": "head", "submitted_at": "2026-10-21T00:00:00Z", "user": map[string]any{"login": "reviewer"}}})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			send([]any{})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			app := 15368
			head := "head"
			if mode == "wrong check app" {
				app = 42
			}
			if mode == "wrong check head" {
				head = "old"
			}
			total := 1
			if mode == "check count mismatch" {
				total = 2
			}
			send(map[string]any{"total_count": total, "check_runs": []any{map[string]any{"id": 7, "name": "go", "status": "completed", "conclusion": "success", "head_sha": head, "details_url": "https://github.com/owner/repo/actions/runs/99/job/7", "app": map[string]any{"id": app}}}})
		case r.URL.Path == "/repos/owner/repo/actions/jobs/7":
			checkURL := "http://" + r.Host + "/repos/owner/repo/check-runs/7"
			if mode == "forged details URL" {
				checkURL = "http://" + r.Host + "/repos/owner/repo/check-runs/999"
			}
			send(map[string]any{"run_id": 99, "check_run_url": checkURL, "head_sha": "head", "name": "go", "status": "completed", "conclusion": "success"})
		case r.URL.Path == "/repos/owner/repo/actions/runs/99":
			f.runReads++
			workflow := 321038296
			if mode == "wrong workflow" {
				workflow = 999
			}
			send(map[string]any{"workflow_id": workflow, "head_sha": "head"})
		case r.URL.Path == "/repos/owner/repo/pulls/1":
			f.metadataReads++
			head := "head"
			if mode == "head changes during fetch" && f.metadataReads > 1 {
				head = "new"
			}
			send(map[string]any{"number": 1, "title": "docs", "body": "Root-Cause: docs/readme#example", "state": "open", "updated_at": "2026-10-21T00:00:00Z", "draft": false, "mergeable": true, "changed_files": 1, "head": map[string]any{"sha": head}, "base": map[string]any{"sha": "base", "ref": "main"}, "user": map[string]any{"login": "jfox85"}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	config := greenlane.DefaultConfig()
	config.RequiredChecks = []string{"go"}
	return &GitHubClient{token: "test", owner: "owner", repo: "repo", apiBase: server.URL, client: server.Client(), config: config}, f
}
func TestGitHubSnapshotPaginationAndCIIdentity(t *testing.T) {
	c, f := apiClient(t, "two thread pages")
	pr, err := c.GetPR(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.SnapshotConsistent || !pr.ReviewThreadsComplete || len(pr.ReviewThreads) != 2 || f.threadPages != 2 || f.runReads != 1 {
		t.Fatalf("incomplete snapshot: %+v %+v", pr, f)
	}
	if pr.CheckRuns[0].WorkflowID != 321038296 || pr.Reviews[0].SubmittedAt.IsZero() {
		t.Fatal("lost check or review identity")
	}
	result := greenlane.NewEvaluator(c.config).Evaluate(pr, nil, nil)
	if !result.AdvisoryCandidate || result.Verdict != greenlane.VerdictWouldMerge {
		t.Fatalf("reviewed candidate should qualify without provenance: %+v", result)
	}
}
func TestGitHubCollectionFailsClosed(t *testing.T) {
	for _, mode := range []string{"graphql partial error", "graphql null", "graphql stale", "missing cursor", "repeated cursor", "thread count mismatch", "missing file page", "review page error", "check count mismatch"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := apiClient(t, mode)
			if _, err := c.GetPR(context.Background(), 1); err == nil {
				t.Fatal("partial or stale API data admitted")
			}
		})
	}
}
func TestGitHubChangedSnapshotAndWrongSourcesCannotBeCandidates(t *testing.T) {
	for _, mode := range []string{"head changes during fetch", "wrong check app", "wrong check head", "wrong workflow", "forged details URL"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := apiClient(t, mode)
			pr, err := c.GetPR(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			r := greenlane.NewEvaluator(c.config).Evaluate(pr, nil, nil)
			if r.AdvisoryCandidate {
				t.Fatalf("%s admitted: %+v", mode, r)
			}
		})
	}
}
func TestDuplicateDiscoveryIncludesBothRenamePaths(t *testing.T) {
	c, _ := apiClient(t, "renamed duplicate")
	prs, err := c.GetOpenPRs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || fmt.Sprint(prs[0].Files) != "[src/new.go src/old.go]" || prs[0].RootCause != "docs/readme#example" {
		t.Fatalf("incomplete duplicates: %+v", prs)
	}
}
func TestEvidenceComparisonDetectsReviewAndCheckChanges(t *testing.T) {
	c, _ := apiClient(t, "")
	a, err := c.GetPR(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.GetPR(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sameEvidence(a, b) {
		t.Fatal("same snapshots differ")
	}
	b.ReviewThreads[0].Resolved = false
	if sameEvidence(a, b) {
		t.Fatal("thread change not detected")
	}
	b.ReviewThreads[0].Resolved = true
	b.Reviews[0].State = "CHANGES_REQUESTED"
	if sameEvidence(a, b) {
		t.Fatal("review change not detected")
	}
	b.Reviews[0].State = "APPROVED"
	b.CheckRuns[0].Conclusion = "failure"
	if sameEvidence(a, b) {
		t.Fatal("CI change not detected")
	}
}
