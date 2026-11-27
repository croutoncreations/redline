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
	// Graph: main is oldBase -> newBase. The PR commit "reviewed" has parent
	// oldBase; head merges newBase into it. "stranger" is unrelated.
	parents := map[string][]string{
		reviewedSHA: {oldBaseSHA},
		headSHA:     {reviewedSHA, newBaseSHA},
		strangerSHA: {oldBaseSHA},
	}
	if mode == "reviewed commit merged another branch" {
		parents[reviewedSHA] = []string{oldBaseSHA, strangerSHA}
	}
	if mode == "reviewed commit merged a later main commit" {
		// A legitimate "merge main into PR": the second parent is on main.
		parents[reviewedSHA] = []string{oldBaseSHA, newBaseSHA}
	}
	onMain := map[string]bool{oldBaseSHA: true, newBaseSHA: true, "main": true}
	mainMerges := mode == "main absorbed a merge"
	ancestorOf := func(a, b string) bool { // a is an ancestor of (or equal to) b
		if a == b {
			return true
		}
		switch b {
		case "main", newBaseSHA:
			return a == oldBaseSHA || a == newBaseSHA
		case headSHA:
			return a == reviewedSHA || a == oldBaseSHA || a == newBaseSHA
		case reviewedSHA, strangerSHA:
			return a == oldBaseSHA
		}
		return false
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
			a, b := parts[0], parts[1]
			if a == "main" && !onMain[b] {
				// merge base of main and a PR commit
				base := oldBaseSHA
				if b == headSHA {
					base = newBaseSHA
				}
				send(map[string]any{"status": "diverged", "merge_base_commit": map[string]string{"sha": base}, "total_commits": 1, "commits": []any{}})
				return
			}
			status := "diverged"
			mb := oldBaseSHA
			if a == b {
				status, mb = "identical", a
			} else if ancestorOf(a, b) {
				status, mb = "ahead", a
			}
			if mode == "reviewed commit merged another branch" && a == strangerSHA && b == "main" {
				// GitHub reports "ahead" for linear-history paging, but the
				// merge base shows stranger is not on main.
				send(map[string]any{"status": "ahead", "merge_base_commit": map[string]string{"sha": oldBaseSHA}, "total_commits": 0, "commits": []any{}})
				return
			}
			var commits []any
			if status == "ahead" && b == "main" && a == oldBaseSHA {
				p := []any{map[string]string{"sha": oldBaseSHA}}
				if mainMerges {
					p = append(p, map[string]string{"sha": strangerSHA})
				}
				commits = []any{map[string]any{"parents": p}}
			}
			send(map[string]any{"status": status, "merge_base_commit": map[string]string{"sha": mb}, "total_commits": len(commits), "commits": commits})
		case strings.Contains(r.URL.Path, "/git/commits/"):
			sha := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			ps, ok := parents[sha]
			if !ok {
				t.Errorf("unexpected commit %s", sha)
				w.WriteHeader(404)
				return
			}
			var out []any
			for _, p := range ps {
				out = append(out, map[string]string{"sha": p})
			}
			send(map[string]any{"sha": sha, "parents": out})
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
	for _, mode := range []string{"reviewed commit merged another branch", "main absorbed a merge"} {
		got, err := carryoverClient(t, mode).reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(reviewedSHA)})
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: carried over a non-linear history: %v %v", mode, got, err)
		}
	}
	for _, mode := range []string{"truncated tree", "compare error"} {
		if _, err := carryoverClient(t, mode).reviewEquivalentHeads(ctx, "main", headSHA, []greenlane.AutomatedReviewComment{rabbitSummary(reviewedSHA)}); err == nil {
			t.Fatalf("%s: incomplete API data admitted", mode)
		}
	}
}

// The reviewed base is found by walking the PR's own first-parent history,
// not from today's main, and only through plain histories.
func TestReviewedBaseNeedsLinearHistory(t *testing.T) {
	ctx := context.Background()
	if base, ok, err := carryoverClient(t, "").reviewedBase(ctx, "main", reviewedSHA); err != nil || !ok || base != oldBaseSHA {
		t.Fatalf("plain PR commit: base=%s ok=%v err=%v", base, ok, err)
	}
	// head merges main (newBase) into the PR: its base is that merged main commit.
	if base, ok, err := carryoverClient(t, "").reviewedBase(ctx, "main", headSHA); err != nil || !ok || base != newBaseSHA {
		t.Fatalf("main-merge head: base=%s ok=%v err=%v", base, ok, err)
	}
	// Merging main into the PR is fine: the base is the merged main commit.
	if base, ok, err := carryoverClient(t, "reviewed commit merged a later main commit").reviewedBase(ctx, "main", reviewedSHA); err != nil || !ok || base != newBaseSHA {
		t.Fatalf("merge of main: base=%s ok=%v err=%v", base, ok, err)
	}
	// A PR commit that merged some other branch has no trustworthy base.
	if _, ok, err := carryoverClient(t, "reviewed commit merged another branch").reviewedBase(ctx, "main", reviewedSHA); err != nil || ok {
		t.Fatalf("merge of another branch accepted: ok=%v err=%v", ok, err)
	}
	// A merge commit on main after the base could have absorbed PR commits.
	if _, ok, err := carryoverClient(t, "main absorbed a merge").reviewedBase(ctx, "main", reviewedSHA); err != nil || ok {
		t.Fatalf("merge on main accepted: ok=%v err=%v", ok, err)
	}
	// A main commit is never a "reviewed PR commit".
	if _, ok, err := carryoverClient(t, "").reviewedBase(ctx, "main", oldBaseSHA); err != nil || ok {
		t.Fatalf("main commit accepted as reviewed PR commit: ok=%v err=%v", ok, err)
	}
	if _, _, err := carryoverClient(t, "compare error").reviewedBase(ctx, "main", reviewedSHA); err == nil {
		t.Fatal("API error admitted")
	}
}
