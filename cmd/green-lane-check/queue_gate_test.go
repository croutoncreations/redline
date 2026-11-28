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

// fullGateServer serves a complete, consistent merge group for PR #12. The
// PR evidence endpoints (/pulls/...) fail, so only paths that skip the full
// evaluation can pass.
func fullGateServer(t *testing.T, entries func(read int) map[string]any) (*GitHubClient, *int) {
	t.Helper()
	reads, pulls := 0, 0
	mb := "6666666666666666666666666666666666666666"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/graphql":
			reads++
			send(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": entries(reads)}}})
		case strings.Contains(r.URL.Path, "/commits/"):
			send(map[string]any{"sha": qHead, "parents": []any{map[string]any{"sha": qBase}}})
		case strings.HasSuffix(r.URL.Path, "/compare/"+qBase+"..."+qHead):
			send(map[string]any{"status": "ahead", "files": []any{map[string]any{"filename": "docs/a.md", "status": "modified", "sha": "b1"}}})
		case strings.HasSuffix(r.URL.Path, "/compare/"+qBase+"..."+qPR):
			send(map[string]any{"status": "ahead", "merge_base_commit": map[string]string{"sha": mb}})
		case strings.HasSuffix(r.URL.Path, "/compare/"+mb+"..."+qBase):
			send(map[string]any{"status": "identical", "files": []any{}})
		case strings.HasSuffix(r.URL.Path, "/compare/"+mb+"..."+qPR):
			send(map[string]any{"status": "ahead", "files": []any{map[string]any{"filename": "docs/a.md", "status": "modified", "sha": "b1"}}})
		default:
			if strings.Contains(r.URL.Path, "/pulls/") {
				pulls++
			}
			w.WriteHeader(502)
		}
	}))
	t.Cleanup(server.Close)
	return &GitHubClient{token: "t", owner: "o", repo: "r", apiBase: server.URL, client: server.Client()}, &pulls
}

func TestRunQueueGateEnqueuers(t *testing.T) {
	ctx := context.Background()
	cfg := greenlane.DefaultConfig()
	t.Setenv("GITHUB_EVENT_NAME", "merge_group")
	event := writeEvent(t, map[string]any{"merge_group": map[string]any{"base_sha": qBase, "head_sha": qHead, "head_ref": "refs/heads/gh-readonly-queue/main/pr-12-" + qBase}})
	as := func(typ, login string, id int) func(int) map[string]any {
		return func(int) map[string]any {
			n := queuedNode()
			n["mergeQueueEntry"].(map[string]any)["enqueuer"] = map[string]any{"__typename": typ, "login": login, "databaseId": id}
			return n
		}
	}

	// Jon's own enqueue passes on path and binding evidence alone, even
	// though every PR-evidence endpoint is failing.
	c, pulls := fullGateServer(t, as("User", "jfox85", 441017))
	if err := runQueueGate(ctx, c, cfg, event); err != nil {
		t.Fatalf("Jon's enqueue blocked: %v", err)
	}
	if *pulls != 0 {
		t.Fatalf("Jon's enqueue read PR evidence %d times", *pulls)
	}

	// Anyone else who isn't the App is rejected without an evaluation.
	c, pulls = fullGateServer(t, as("User", "newwriter", 5))
	if err := runQueueGate(ctx, c, cfg, event); err == nil || !strings.Contains(err.Error(), "neither Jon nor Green Lane") {
		t.Fatalf("other writer's enqueue: %v", err)
	}
	if *pulls != 0 {
		t.Fatal("rejected enqueuer was evaluated")
	}

	// The App's enqueue needs the full evaluation, so failing PR evidence
	// blocks it.
	c, pulls = fullGateServer(t, as("Bot", "green-lane-merge", 362142609))
	if err := runQueueGate(ctx, c, cfg, event); err == nil {
		t.Fatal("App enqueue passed without PR evidence")
	}
	if *pulls == 0 {
		t.Fatal("App enqueue was not evaluated")
	}
}

func TestQueueGateRereadsEntry(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GITHUB_EVENT_NAME", "merge_group")
	event := writeEvent(t, map[string]any{"merge_group": map[string]any{"base_sha": qBase, "head_sha": qHead, "head_ref": "refs/heads/gh-readonly-queue/main/pr-12-" + qBase}})
	mb := "6666666666666666666666666666666666666666"
	for name, tc := range map[string]struct {
		later func(map[string]any)
		want  string
	}{
		"hold label added": {func(n map[string]any) {
			n["labels"] = map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"name": "green-lane-hold"}}}
		}, "label"},
		"re-queued by someone else": {func(n map[string]any) {
			n["mergeQueueEntry"].(map[string]any)["enqueuer"] = map[string]any{"__typename": "User", "login": "jfox85", "databaseId": 441017}
		}, "changed while"},
		"new head": {func(n map[string]any) { n["headRefOid"] = qBase }, "changed while"},
	} {
		t.Run(name, func(t *testing.T) {
			reads := 0
			entry := func() map[string]any {
				n := queuedNode()
				n["mergeQueueEntry"].(map[string]any)["enqueuer"] = map[string]any{"__typename": "Bot", "login": "green-lane-merge"}
				if reads > 1 {
					tc.later(n)
				}
				return n
			}
			c := &GitHubClient{token: "t", owner: "o", repo: "r", client: http.DefaultClient}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
				switch {
				case r.URL.Path == "/graphql":
					reads++
					send(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": entry()}}})
				case strings.Contains(r.URL.Path, "/commits/"):
					send(map[string]any{"sha": qHead, "parents": []any{map[string]any{"sha": qBase}}})
				case strings.HasSuffix(r.URL.Path, "/compare/"+qBase+"..."+qPR):
					send(map[string]any{"status": "ahead", "merge_base_commit": map[string]string{"sha": mb}})
				case strings.Contains(r.URL.Path, "/compare/"):
					send(map[string]any{"status": "ahead", "files": []any{map[string]any{"filename": "docs/a.md", "status": "modified", "sha": "b1"}}})
				default:
					w.WriteHeader(404)
				}
			}))
			t.Cleanup(server.Close)
			c.apiBase, c.client = server.URL, server.Client()
			// Stub the PR evidence: the evaluation itself is covered elsewhere.
			getPR = func(*GitHubClient, context.Context, int) (greenlane.PRInfo, error) {
				return greenlane.PRInfo{Number: 12, HeadSHA: qPR, SnapshotConsistent: true}, nil
			}
			t.Cleanup(func() { getPR = (*GitHubClient).GetPR })
			err := runQueueGate(ctx, c, greenlane.DefaultConfig(), event)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
			if reads < 2 {
				t.Fatal("entry was not re-read")
			}
		})
	}
}
