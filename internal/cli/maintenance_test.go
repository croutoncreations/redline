package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

func TestTaskAddKindPRMaintenanceDefaultsToRecurringWithoutPrompt(t *testing.T) {
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add",
		"--name", "Maintain PRs", "--profile", "claude-repo", "--kind", "pr_maintenance", "--min-interval", "12h"},
		strings.NewReader(""), &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if api.created["kind"] != "pr_maintenance" || api.created["type"] != "recurring" ||
		api.created["min_interval"] != "12h" || api.created["prompt"] != nil {
		t.Fatalf("create body = %#v", api.created)
	}
	// Standard tasks still require a prompt and still default to one_off.
	stderr.Reset()
	if exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add", "--name", "n", "--profile", "p", "--kind", "standard"},
		strings.NewReader(""), &stdout, &stderr, time.Now); exit != 1 || !strings.Contains(stderr.String(), "--prompt or --prompt-file is required") {
		t.Fatalf("standard without prompt: exit=%d stderr=%s", exit, stderr.String())
	}
	stderr.Reset()
	if exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add", "--name", "n", "--profile", "p", "--kind", "nightly", "--prompt", "x"},
		strings.NewReader(""), &stdout, &stderr, time.Now); exit != 1 || !strings.Contains(stderr.String(), "--kind must be") {
		t.Fatalf("bad kind: exit=%d stderr=%s", exit, stderr.String())
	}
	if exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add", "--name", "n", "--profile", "p", "--prompt", "x"},
		strings.NewReader(""), &stdout, &stderr, time.Now); exit != 0 || api.created["type"] != "one_off" || api.created["kind"] != "standard" {
		t.Fatalf("standard default: exit=%d body=%#v", exit, api.created)
	}
}

func TestRunAttributeExitCodes(t *testing.T) {
	var gotQuery string
	attributable := true
	fail := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/runs/attribution", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "boom"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"attributable": attributable, "reason": "because", "run_id": "run-1"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		exit := cli.Run(append([]string{"--api", server.URL, "run", "attribute"}, args...), &stdout, &stderr, time.Now)
		return exit, stdout.String(), stderr.String()
	}
	if exit, stdout, _ := run("--branch", "redline/run-1", "--repo", "/repo"); exit != 0 || !strings.Contains(stdout, `"attributable": true`) {
		t.Fatalf("attributable: exit=%d stdout=%s", exit, stdout)
	}
	if !strings.Contains(gotQuery, "branch=redline%2Frun-1") || !strings.Contains(gotQuery, "repository=%2Frepo") {
		t.Fatalf("query = %s", gotQuery)
	}
	attributable = false
	if exit, stdout, _ := run("--branch", "redline/run-1", "--repo", "/repo"); exit != 3 || !strings.Contains(stdout, `"reason": "because"`) {
		t.Fatalf("not attributable: exit=%d stdout=%s", exit, stdout)
	}
	fail = true
	if exit, _, stderr := run("--branch", "redline/run-1", "--repo", "/repo"); exit != 1 || !strings.Contains(stderr, "boom") {
		t.Fatalf("error: exit=%d stderr=%s", exit, stderr)
	}
	if exit, _, _ := run("--repo", "/repo"); exit != 1 {
		t.Fatalf("missing branch exit = %d", exit)
	}
	// The repository defaults to the git top level of the current directory.
	fail, attributable = false, true
	repo := gitRepo(t)
	t.Chdir(repo)
	if exit, _, stderr := run("--branch", "redline/run-1"); exit != 0 || !strings.Contains(gotQuery, "repository="+url.QueryEscape(repo)) {
		t.Fatalf("default repo: exit=%d query=%s stderr=%s", exit, gotQuery, stderr)
	}
}

func TestLeaseAcquireAndRelease(t *testing.T) {
	var acquired map[string]string
	var releasedQuery string
	conflict := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/branch-leases", func(w http.ResponseWriter, r *http.Request) {
		if conflict {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "branch is leased by active run other"})
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&acquired)
		_ = json.NewEncoder(w).Encode(map[string]any{"repository": acquired["repository"], "branch": acquired["branch"], "run_id": acquired["run_id"]})
	})
	mux.HandleFunc("DELETE /v1/branch-leases", func(w http.ResponseWriter, r *http.Request) {
		releasedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	t.Setenv("REDLINE_RUN_ID", "env-run")
	var stdout, stderr bytes.Buffer
	if exit := cli.Run([]string{"--api", server.URL, "lease", "acquire", "--branch", "redline/run-1", "--repo", "/repo"}, &stdout, &stderr, time.Now); exit != 0 {
		t.Fatalf("acquire exit=%d stderr=%s", exit, stderr.String())
	}
	if acquired["run_id"] != "env-run" || acquired["branch"] != "redline/run-1" || acquired["repository"] != "/repo" {
		t.Fatalf("acquire body = %#v", acquired)
	}
	if exit := cli.Run([]string{"--api", server.URL, "lease", "release", "--branch", "redline/run-1", "--repo", "/repo", "--run", "explicit"}, &stdout, &stderr, time.Now); exit != 0 {
		t.Fatalf("release exit=%d stderr=%s", exit, stderr.String())
	}
	if !strings.Contains(releasedQuery, "run_id=explicit") || !strings.Contains(releasedQuery, "branch=redline%2Frun-1") {
		t.Fatalf("release query = %s", releasedQuery)
	}
	conflict = true
	stderr.Reset()
	if exit := cli.Run([]string{"--api", server.URL, "lease", "acquire", "--branch", "b", "--repo", "/repo"}, &stdout, &stderr, time.Now); exit != 1 || !strings.Contains(stderr.String(), "409") {
		t.Fatalf("conflict exit=%d stderr=%s", exit, stderr.String())
	}
	if exit := cli.Run([]string{"--api", server.URL, "lease", "steal"}, &stdout, &stderr, time.Now); exit != 1 {
		t.Fatalf("bad subcommand exit = %d", exit)
	}
}
