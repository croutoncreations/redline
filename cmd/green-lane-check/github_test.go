package main

import (
	"context"
	"encoding/json"
	"errors"
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
				w.Header().Set("X-GitHub-Request-Id", "REQ-2AM-403")
				w.WriteHeader(403)
				send(map[string]any{
					"message":           "API rate limit exceeded",
					"documentation_url": "https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api",
				})
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
			_, err := c.GetPR(context.Background(), 1)
			if err == nil {
				t.Fatal("partial or stale API data admitted")
			}
			if mode == "review page error" {
				for _, want := range []string{
					"GitHub API GET " + c.apiBase + "/repos/owner/repo/pulls/1/reviews?per_page=100&page=1 returned 403 Forbidden",
					"API rate limit exceeded",
					"request ID REQ-2AM-403",
					"check GITHUB_TOKEN permissions and GitHub API rate limits",
					"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api",
				} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not contain %q", err, want)
					}
				}
				if strings.Contains(err.Error(), c.token) {
					t.Fatalf("error leaked bearer token: %q", err)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGitHubTransportErrorNamesOperationAndEndpoint(t *testing.T) {
	const token = "secret-token-that-must-not-appear"
	c := &GitHubClient{
		token: token,
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: connection refused")
		})},
	}
	_, err := c.doRequest(context.Background(), "GET", "https://api.github.test/repos/owner/repo/pulls/42")
	if err == nil {
		t.Fatal("transport failure returned no error")
	}
	want := "GitHub API GET https://api.github.test/repos/owner/repo/pulls/42 failed: dial tcp: connection refused; check network connectivity and GitHub availability"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked bearer token: %q", err)
	}
}

func TestGitHubAPIErrorValuesAreSafeForLogs(t *testing.T) {
	got := githubAPIErrorValue("first line\nsecond\tline "+strings.Repeat("x", 20), 24)
	if got != "first line second line x..." {
		t.Fatalf("sanitized error value = %q", got)
	}
}

func TestGitHubAPIRecoveryHints(t *testing.T) {
	tests := map[int]string{
		http.StatusUnauthorized:        "check that GITHUB_TOKEN is valid and can access the repository",
		http.StatusForbidden:           "check GITHUB_TOKEN permissions and GitHub API rate limits",
		http.StatusNotFound:            "check REPO_OWNER, REPO_NAME, and GITHUB_TOKEN repository access",
		http.StatusTooManyRequests:     "retry after the GitHub API rate limit resets",
		http.StatusInternalServerError: "retry the request or check https://www.githubstatus.com",
		http.StatusBadGateway:          "retry the request or check https://www.githubstatus.com",
		http.StatusServiceUnavailable:  "retry the request or check https://www.githubstatus.com",
		http.StatusGatewayTimeout:      "retry the request or check https://www.githubstatus.com",
		http.StatusUnprocessableEntity: "",
	}
	for status, want := range tests {
		if got := githubAPIRecovery(status); got != want {
			t.Errorf("githubAPIRecovery(%d) = %q, want %q", status, got, want)
		}
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

func TestAutomatedReviewEditorJoin(t *testing.T) {
	for _, mode := range []string{"valid", "human editor", "body changed", "node missing", "partial error", "pagination", "repeated page"} {
		t.Run(mode, func(t *testing.T) {
			body := "<!-- coderabbit-ai-generated:v1 -->\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->"
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				send := func(v any) {
					if err := json.NewEncoder(w).Encode(v); err != nil {
						t.Error(err)
					}
				}
				if r.Method == "GET" {
					pages++
					if (mode == "pagination" && pages == 1) || mode == "repeated page" {
						rows := []any{}
						for i := 1; i <= 100; i++ {
							rows = append(rows, map[string]any{"id": 100 + i})
						}
						send(rows)
						return
					}
					send([]any{map[string]any{"id": 42, "node_id": "node", "body": body, "updated_at": "2026-01-01T00:00:00Z", "user": map[string]any{"id": 136622811, "login": "coderabbitai[bot]", "type": "Bot"}, "performed_via_github_app": map[string]any{"id": 347564}}})
					return
				}
				editor := map[string]any{"__typename": "Bot", "login": "coderabbitai", "databaseId": 136622811}
				if mode == "human editor" {
					editor = map[string]any{"__typename": "User", "login": "author"}
				}
				node := map[string]any{"id": "node", "databaseId": 42, "body": body, "updatedAt": "2026-01-01T00:00:00Z", "lastEditedAt": "2026-01-01T00:00:00Z", "editor": editor}
				if mode == "body changed" {
					node["body"] = "changed"
				}
				response := map[string]any{"data": map[string]any{"nodes": []any{node}}}
				if mode == "node missing" {
					response["data"] = map[string]any{"nodes": []any{nil}}
				}
				if mode == "partial error" {
					response["errors"] = []any{map[string]any{"message": "denied"}}
				}
				send(response)
			}))
			defer server.Close()
			c := &GitHubClient{apiBase: server.URL, client: server.Client(), owner: "owner", repo: "repo"}
			got, err := c.automatedReviews(context.Background(), 1)
			wantErr := mode == "body changed" || mode == "node missing" || mode == "partial error" || mode == "repeated page"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v want error %v", err, wantErr)
			}
			if !wantErr {
				if len(got) != 1 || got[0].ID != 42 {
					t.Fatalf("unexpected evidence: %d", len(got))
				}
				if mode == "human editor" && got[0].EditorType != "User" {
					t.Fatal("editor omitted")
				}
				if mode == "pagination" && pages != 2 {
					t.Fatal("pagination incomplete")
				}
			}
		})
	}
}
