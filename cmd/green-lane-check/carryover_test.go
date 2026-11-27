package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/greenlane"
)

const (
	headSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reviewedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldBaseSHA  = "cccccccccccccccccccccccccccccccccccccccc"
	newBaseSHA  = "dddddddddddddddddddddddddddddddddddddddd"
	strangerSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func carryoverClient(t *testing.T, mode string) *GitHubClient {
	t.Helper()
	entry := func(path, sha string) map[string]string {
		return map[string]string{"path": path, "mode": "100644", "type": "blob", "sha": sha}
	}
	trees := map[string][]map[string]string{
		oldBaseSHA:  {entry("a.go", "a1"), entry("docs.md", "d1")},
		reviewedSHA: {entry("a.go", "a2"), entry("docs.md", "d1")},
		newBaseSHA:  {entry("a.go", "a1"), entry("docs.md", "d2")},
		headSHA:     {entry("a.go", "a2"), entry("docs.md", "d2")},
	}
	if mode == "PR changed after review" {
		trees[headSHA] = []map[string]string{entry("a.go", "a3"), entry("docs.md", "d2")}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case strings.Contains(r.URL.Path, "/compare/"):
			spec := r.URL.Path[strings.Index(r.URL.Path, "/compare/")+len("/compare/"):]
			parts := strings.SplitN(spec, "...", 2)
			if mode == "compare error" {
				w.WriteHeader(502)
				return
			}
			switch {
			case parts[0] == "main" && parts[1] == headSHA:
				send(map[string]any{"status": "ahead", "merge_base_commit": map[string]string{"sha": newBaseSHA}})
			case parts[0] == "main" && parts[1] == reviewedSHA:
				send(map[string]any{"status": "diverged", "merge_base_commit": map[string]string{"sha": oldBaseSHA}})
			case parts[0] == reviewedSHA && parts[1] == headSHA:
				send(map[string]any{"status": "ahead", "merge_base_commit": map[string]string{"sha": reviewedSHA}})
			case parts[0] == strangerSHA && parts[1] == headSHA:
				send(map[string]any{"status": "diverged", "merge_base_commit": map[string]string{"sha": oldBaseSHA}})
			default:
				t.Errorf("unexpected compare %s", spec)
				w.WriteHeader(404)
			}
		case strings.Contains(r.URL.Path, "/git/trees/"):
			sha := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			tree, ok := trees[sha]
			if !ok {
				t.Errorf("unexpected tree %s", sha)
				w.WriteHeader(404)
				return
			}
			send(map[string]any{"sha": sha, "truncated": mode == "truncated tree", "tree": tree})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return &GitHubClient{token: "test", owner: "owner", repo: "repo", apiBase: server.URL, client: server.Client()}
}

func rabbitSummary(sha string) greenlane.AutomatedReviewComment {
	return greenlane.AutomatedReviewComment{ID: 42, UserID: 136622811, User: "coderabbitai[bot]", UserType: "Bot", AppID: 347564, UpdatedAt: "2026-01-01T00:00:00Z",
		Body: "<!-- coderabbit-ai-generated:v1 -->\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->\n<!-- final_review_risk_coverage:{\"sourceCommitId\":\"" + sha + "\",\"coveredCommitId\":\"" + sha + "\",\"kind\":\"reviewed\"} -->"}
}

func TestReviewEquivalentHeads(t *testing.T) {
	ctx := context.Background()
	got, err := carryoverClient(t, "").reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(reviewedSHA)})
	if err != nil || len(got) != 1 || got[0] != reviewedSHA {
		t.Fatalf("clean branch update not carried over: %v %v", got, err)
	}
	got, err = carryoverClient(t, "PR changed after review").reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(reviewedSHA)})
	if err != nil || len(got) != 0 {
		t.Fatalf("changed PR carried over: %v %v", got, err)
	}
	got, err = carryoverClient(t, "").reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(strangerSHA)})
	if err != nil || len(got) != 0 {
		t.Fatalf("non-ancestor commit carried over: %v %v", got, err)
	}
	untrusted := rabbitSummary(reviewedSHA)
	untrusted.UserType = "User"
	got, err = carryoverClient(t, "").reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{untrusted})
	if err != nil || len(got) != 0 {
		t.Fatalf("untrusted summary used: %v %v", got, err)
	}
	for _, mode := range []string{"truncated tree", "compare error"} {
		if _, err := carryoverClient(t, mode).reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(reviewedSHA)}); err == nil {
			t.Fatalf("%s: incomplete API data admitted", mode)
		}
	}
}
