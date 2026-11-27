package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func driftClient(t *testing.T, mode string) *GitHubClient {
	t.Helper()
	const mb = "cccccccccccccccccccccccccccccccccccccccc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		spec := r.URL.Path[strings.Index(r.URL.Path, "/compare/")+len("/compare/"):]
		if mode == "api error" {
			w.WriteHeader(502)
			return
		}
		switch {
		case strings.HasPrefix(spec, "main..."):
			send(map[string]any{"status": "diverged", "merge_base_commit": map[string]string{"sha": mb}})
		case spec == mb+"...main":
			var files []any
			n := 2
			if mode == "300 files" {
				n = 300
			}
			for i := 0; i < n; i++ {
				files = append(files, map[string]string{"filename": fmt.Sprintf("f%d.go", i)})
			}
			if mode == "rename" {
				files = []any{map[string]string{"filename": "new.go", "previous_filename": "internal/apiauth/old.go"}}
			}
			status := "ahead"
			if mode == "diverged" {
				status = "diverged"
			}
			send(map[string]any{"status": status, "files": files})
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return &GitHubClient{token: "t", owner: "o", repo: "r", apiBase: server.URL, client: server.Client()}
}

func TestMainDriftCollection(t *testing.T) {
	ctx := context.Background()
	if files, ok := driftClient(t, "").mainDrift(ctx, "main", "head"); !ok || len(files) != 2 {
		t.Fatalf("got %v %v", files, ok)
	}
	if files, ok := driftClient(t, "rename").mainDrift(ctx, "main", "head"); !ok || len(files) != 2 || files[1] != "internal/apiauth/old.go" {
		t.Fatalf("rename must report both paths: %v %v", files, ok)
	}
	for _, mode := range []string{"300 files", "api error", "diverged"} {
		if _, ok := driftClient(t, mode).mainDrift(ctx, "main", "head"); ok {
			t.Fatalf("%s: incomplete drift reported as known", mode)
		}
	}
}
