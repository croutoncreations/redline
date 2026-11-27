package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/greenlane"
)

const (
	qBase = "2222222222222222222222222222222222222222"
	qHead = "3333333333333333333333333333333333333333"
	qPR   = "4444444444444444444444444444444444444444"
)

func gateClient(t *testing.T, pr map[string]any, compare func(spec string) map[string]any) *GitHubClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/graphql":
			send(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
		case strings.Contains(r.URL.Path, "/commits/"):
			sha := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			send(map[string]any{"sha": sha, "parents": []any{map[string]any{"sha": qBase}}})
		case strings.Contains(r.URL.Path, "/compare/"):
			spec := r.URL.Path[strings.Index(r.URL.Path, "/compare/")+len("/compare/"):]
			if v := compare(spec); v != nil {
				send(v)
				return
			}
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return &GitHubClient{token: "t", owner: "o", repo: "r", apiBase: server.URL, client: server.Client()}
}

func queuedNode() map[string]any {
	return map[string]any{"number": 12, "headRefOid": qPR, "isInMergeQueue": true,
		"mergeQueueEntry": map[string]any{
			"headCommit": map[string]any{"oid": qHead}, "baseCommit": map[string]any{"oid": qBase},
			"enqueuer":   map[string]any{"__typename": "User", "login": "jfox85", "databaseId": 441017},
			"mergeQueue": map[string]any{"configuration": map[string]any{"mergeMethod": "SQUASH", "mergingStrategy": "ALLGREEN", "maximumEntriesToBuild": 1}}},
		"labels": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"name": "docs"}}}}
}

func TestQueueEntry(t *testing.T) {
	ctx := context.Background()
	q, err := gateClient(t, queuedNode(), nil).queueEntry(ctx, 12)
	want := greenlane.QueueConfig{MergeMethod: "SQUASH", MergingStrategy: "ALLGREEN", MaxEntriesBuild: 1}
	if err != nil || q.head != qPR || q.entryHead != qHead || q.entryBase != qBase || q.enqueuerType != "User" || q.enqueuerID != 441017 || q.config != want || len(q.labels) != 1 {
		t.Fatalf("got %+v %v", q, err)
	}
	entry := func(n map[string]any) map[string]any { return n["mergeQueueEntry"].(map[string]any) }
	for name, edit := range map[string]func(map[string]any){
		"not queued":       func(n map[string]any) { n["isInMergeQueue"] = false },
		"no entry":         func(n map[string]any) { n["mergeQueueEntry"] = nil },
		"no head commit":   func(n map[string]any) { entry(n)["headCommit"] = nil },
		"no base commit":   func(n map[string]any) { entry(n)["baseCommit"] = nil },
		"no enqueuer":      func(n map[string]any) { entry(n)["enqueuer"] = nil },
		"no queue config":  func(n map[string]any) { entry(n)["mergeQueue"] = map[string]any{"configuration": nil} },
		"other PR":         func(n map[string]any) { n["number"] = 13 },
		"short head":       func(n map[string]any) { n["headRefOid"] = "abc" },
		"labels truncated": func(n map[string]any) { n["labels"].(map[string]any)["totalCount"] = 101 },
	} {
		n := queuedNode()
		edit(n)
		if _, err := gateClient(t, n, nil).queueEntry(ctx, 12); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := gateClient(t, nil, nil).queueEntry(ctx, 12); err == nil {
		t.Error("missing PR accepted")
	}
}

func TestParentsOf(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/commits/" + qHead:
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": qHead, "parents": []any{map[string]any{"sha": qBase}}})
		case "/repos/o/r/commits/" + qPR:
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": qBase})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	c := &GitHubClient{token: "t", owner: "o", repo: "r", apiBase: server.URL, client: server.Client()}
	if p, err := c.parentsOf(ctx, qHead); err != nil || len(p) != 1 || p[0] != qBase {
		t.Fatalf("got %v %v", p, err)
	}
	if _, err := c.parentsOf(ctx, qPR); err == nil {
		t.Error("response for another commit accepted")
	}
	if _, err := c.parentsOf(ctx, qBase); err == nil {
		t.Error("API error swallowed")
	}
}

func writeEvent(t *testing.T, v any) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	raw, _ := json.Marshal(v)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunQueueGatePayload(t *testing.T) {
	ctx := context.Background()
	cfg := greenlane.DefaultConfig()
	t.Setenv("GITHUB_EVENT_NAME", "push")
	if err := runQueueGate(ctx, nil, cfg, "/nonexistent"); err != nil {
		t.Fatalf("non-queue run must pass: %v", err)
	}
	t.Setenv("GITHUB_EVENT_NAME", "merge_group")
	if err := runQueueGate(ctx, nil, cfg, "/nonexistent"); err == nil {
		t.Fatal("unreadable payload accepted")
	}
	good := map[string]any{"base_sha": qBase, "head_sha": qHead, "head_ref": "refs/heads/gh-readonly-queue/main/pr-12-" + qBase}
	for name, edit := range map[string]func(map[string]any){
		"bad ref":  func(m map[string]any) { m["head_ref"] = "refs/heads/main" },
		"bad base": func(m map[string]any) { m["base_sha"] = "2222" },
		"bad head": func(m map[string]any) { m["head_sha"] = "" },
	} {
		mg := map[string]any{}
		for k, v := range good {
			mg[k] = v
		}
		edit(mg)
		if err := runQueueGate(ctx, gateClient(t, queuedNode(), nil), cfg, writeEvent(t, map[string]any{"merge_group": mg})); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Wrong queue entry: rejected before the PR is evaluated (the fake API
	// would fail the test on any /pulls request).
	n := queuedNode()
	n["mergeQueueEntry"].(map[string]any)["headCommit"] = map[string]any{"oid": qPR}
	c := gateClient(t, n, func(spec string) map[string]any {
		return map[string]any{"status": "ahead", "files": []any{}, "merge_base_commit": map[string]string{"sha": qBase}}
	})
	if err := runQueueGate(ctx, c, cfg, writeEvent(t, map[string]any{"merge_group": good})); err == nil {
		t.Fatal("merge group for another entry accepted")
	}
}
