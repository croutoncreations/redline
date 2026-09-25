package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/cli"
)

type taskAPI struct {
	t        *testing.T
	profiles string
	mu       sync.Mutex
	created  map[string]any
	creates  int
}

func (api *taskAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/profiles", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, api.profiles)
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.creates++
		api.created = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&api.created); err != nil {
			api.t.Errorf("decode create body: %v", err)
		}
		response := map[string]any{
			"id": "task-123", "name": api.created["name"], "prompt": api.created["prompt"],
			"execution_profile_id": api.created["execution_profile_id"],
			"type":                 api.created["type"], "dispatch_tier": api.created["dispatch_tier"],
			"priority": api.created["priority"], "enabled": api.created["enabled"] != false, "state": "queued",
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	})
	return mux
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if output, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func profilesJSON(t *testing.T, profiles ...map[string]any) string {
	t.Helper()
	data, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTaskAddWithFlagsCreatesTaskWithoutYAML(t *testing.T) {
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add",
		"--name", "Parser tests", "--prompt", "-", "--profile", "claude-repo",
		"--tier", "well_behind", "--priority", "70", "--disabled", "--json"},
		strings.NewReader("Add table tests for the parser.\n"), &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if api.created["prompt"] != "Add table tests for the parser.\n" ||
		api.created["execution_profile_id"] != "claude-repo" ||
		api.created["dispatch_tier"] != "well_behind" || api.created["type"] != "one_off" ||
		api.created["priority"] != float64(70) || api.created["enabled"] != false {
		t.Fatalf("create body = %#v", api.created)
	}
	var output struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout is not JSON: %v %s", err, stdout.String())
	}
	if output.ID != "task-123" {
		t.Fatalf("output = %#v", output)
	}
}

func TestTaskAddWithFlagsValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"task", "add", "--profile", "p", "--prompt", "x"}, "--name is required"},
		{[]string{"task", "add", "--name", "n", "--prompt", "x"}, "--profile is required"},
		{[]string{"task", "add", "--name", "n", "--profile", "p"}, "--prompt or --prompt-file is required"},
		{[]string{"task", "add", "--name", "n", "--profile", "p", "--prompt", "x", "stray"}, "unexpected argument"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		exit := cli.RunWithInput(append([]string{"--api", "http://127.0.0.1:1"}, test.args...),
			strings.NewReader(""), &stdout, &stderr, time.Now)
		if exit != 1 || !strings.Contains(stderr.String(), test.want) {
			t.Errorf("%v: exit=%d stderr=%s", test.args, exit, stderr.String())
		}
	}
}

func TestTaskAddFileRejectsIgnoredDisabledAndOtherFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, extra := range [][]string{{"--disabled"}, {"--type", "recurring"}, {"--priority", "50"}, {"--default-profile", "p"}} {
		stdout.Reset()
		stderr.Reset()
		args := append([]string{"task", "add", "--file", "would-not-exist.yaml"}, extra...)
		exit := cli.RunWithInput(args, nil, &stdout, &stderr, time.Now)
		if exit != 1 || !strings.Contains(stderr.String(), "--file cannot be combined") {
			t.Errorf("args=%v exit=%d stderr=%s", args, exit, stderr.String())
		}
	}
}

func TestTaskAddPromptValueIsNotMistakenForYAMLMode(t *testing.T) {
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "task", "add", "--name", "Literal flag",
		"--prompt", "--file", "--profile", "p", "--json"}, nil, &stdout, &stderr, time.Now)
	if exit != 0 || api.created["prompt"] != "--file" {
		t.Fatalf("exit=%d stderr=%s body=%#v", exit, stderr.String(), api.created)
	}
}

func TestLaterHookPromptTreatsFlagsAsText(t *testing.T) {
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--profile", "p", "--prompt", "-"},
		strings.NewReader("--profile other-repo do work"), &stdout, &stderr, time.Now)
	if exit != 0 || api.created["execution_profile_id"] != "p" ||
		api.created["prompt"] != "--profile other-repo do work" {
		t.Fatalf("exit=%d stderr=%s body=%#v", exit, stderr.String(), api.created)
	}
}

func TestTaskAddRejectsOversizedStdinPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	large := strings.Repeat("x", 256*1024+1)
	exit := cli.RunWithInput([]string{"--api", "http://127.0.0.1:1", "task", "add",
		"--name", "n", "--profile", "p", "--prompt", "-"}, strings.NewReader(large), &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "exceeds") {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}

func TestLaterResolvesProfileFromRepository(t *testing.T) {
	repo := gitRepo(t)
	nested := filepath.Join(repo, "internal", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	api := &taskAPI{t: t, profiles: profilesJSON(t,
		map[string]any{"id": "codex-same-repo", "harness_type": "codex", "repository": repo},
		map[string]any{"id": "claude-other", "harness_type": "claude-code", "repository": "/elsewhere"},
		map[string]any{"id": "claude-this-repo", "harness_type": "claude-code", "repository": repo + "/"},
	)}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", nested, "--json",
		"finish the parser refactor and run go test ./..."}, nil, &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if api.created["execution_profile_id"] != "claude-this-repo" || api.created["type"] != "one_off" ||
		api.created["dispatch_tier"] != "behind" || api.created["priority"] != float64(50) {
		t.Fatalf("create body = %#v", api.created)
	}
	if name, _ := api.created["name"].(string); !strings.HasPrefix(name, "Later: finish the parser refactor") {
		t.Fatalf("name = %q", name)
	}
	if _, present := api.created["enabled"]; present {
		t.Fatalf("later must not send enabled; body = %#v", api.created)
	}
	var output struct {
		ProfileResolution string `json:"profile_resolution"`
		Repository        string `json:"repository"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.ProfileResolution != "repository" || output.Repository != repo {
		t.Fatalf("output = %#v", output)
	}
}

func TestLaterReadsTextFromStdinAndPrintsNoBypassExplanation(t *testing.T) {
	repo := gitRepo(t)
	api := &taskAPI{t: t, profiles: profilesJSON(t,
		map[string]any{"id": "claude-this-repo", "harness_type": "claude-code", "repository": repo})}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", repo, "--tier", "expiring", "-"},
		strings.NewReader("  write the migration\nand its test  \n"), &stdout, &stderr, time.Now)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if api.created["prompt"] != "write the migration\nand its test" || api.created["dispatch_tier"] != "expiring" {
		t.Fatalf("create body = %#v", api.created)
	}
	if !strings.Contains(stdout.String(), "Queued Redline task task-123") ||
		!strings.Contains(stdout.String(), "behind pace and above your reserve") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestLaterDoesNotSilentlyUseDefaultProfile(t *testing.T) {
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", t.TempDir(),
		"--default-profile", "some-other-repo", "do work"}, nil, &stdout, &stderr, time.Now)
	if exit == 0 || api.creates != 0 {
		t.Fatalf("default fallback must require opt-in: exit=%d body=%#v", exit, api.created)
	}
}

func TestLaterFallsBackToDefaultProfile(t *testing.T) {
	outside := t.TempDir()
	api := &taskAPI{t: t, profiles: "[]"}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", outside,
		"--default-profile", "claude-default", "--allow-default-profile", "--json", "tidy docs"}, nil, &stdout, &stderr, time.Now)
	if exit != 0 || api.created["execution_profile_id"] != "claude-default" {
		t.Fatalf("exit=%d stderr=%s body=%#v", exit, stderr.String(), api.created)
	}
	if !strings.Contains(stdout.String(), `"profile_resolution": "default"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestLaterFailsClearlyWithoutMatchingProfile(t *testing.T) {
	repo := gitRepo(t)
	api := &taskAPI{t: t, profiles: profilesJSON(t,
		map[string]any{"id": "codex-only", "harness_type": "codex", "repository": repo})}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", repo, "x"}, nil, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "no claude-code execution profile uses repository") {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if api.creates != 0 {
		t.Fatal("no task should be created without a profile")
	}
}

func TestLaterRejectsAmbiguousProfilesUnlessDefaultMatches(t *testing.T) {
	repo := gitRepo(t)
	api := &taskAPI{t: t, profiles: profilesJSON(t,
		map[string]any{"id": "claude-b", "harness_type": "claude-code", "repository": repo},
		map[string]any{"id": "claude-a", "harness_type": "claude-code", "repository": repo})}
	server := httptest.NewServer(api.handler())
	defer server.Close()
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", repo, "x"}, nil, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "claude-a, claude-b") || api.creates != 0 {
		t.Fatalf("exit=%d stderr=%s creates=%d", exit, stderr.String(), api.creates)
	}
	stdout.Reset()
	stderr.Reset()
	exit = cli.RunWithInput([]string{"--api", server.URL, "later", "--cwd", repo,
		"--default-profile", "claude-b", "--allow-default-profile", "x"}, nil, &stdout, &stderr, time.Now)
	if exit != 0 || api.created["execution_profile_id"] != "claude-b" {
		t.Fatalf("exit=%d stderr=%s body=%#v", exit, stderr.String(), api.created)
	}
}

func TestLaterRequiresText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.RunWithInput([]string{"--api", "http://127.0.0.1:1", "later"}, strings.NewReader(""), &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "usage: redline later") {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}

func TestRunWatchEmitsOneLinePerNewlyFinishedRun(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/runs/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("baseline") == "true" {
			fmt.Fprint(w, `{"cursor":1,"runs":[]}`)
			return
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("run watch should bound the page with a limit")
		}
		switch polls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"cursor":1,"runs":[]}`)
		default:
			fmt.Fprint(w, `{"cursor":3,"runs":[
				{"id":"r1","task_id":"t1","state":"completed","summary":"done",
				 "artifacts":[{"type":"report","url":"https://example.com/r"},{"type":"pull_request","url":"https://github.com/o/r/pull/123"}]},
				{"id":"r2","task_id":"t2","state":"failed","summary":"tests failed"}]}`)
		}
	})
	mux.HandleFunc("GET /v1/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":%q,"name":"Task %s"}`, r.PathValue("task"), r.PathValue("task"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run([]string{"--api", server.URL, "run", "watch", "--jsonl", "--interval", "100ms", "--count", "2"},
			&stdout, &stderr, time.Now)
	}()
	select {
	case exit := <-done:
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run watch did not exit after --count events")
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first["run_id"] != "r1" || first["status"] != "completed" || first["task"] != "Task t1" ||
		first["pr_url"] != "https://github.com/o/r/pull/123" {
		t.Fatalf("first = %#v", first)
	}
	if second["run_id"] != "r2" || second["status"] != "failed" || second["pr_url"] != nil {
		t.Fatalf("second = %#v", second)
	}
}

func TestRunWatchRequiresJSONL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := cli.Run([]string{"--api", "http://127.0.0.1:1", "run", "watch"}, &stdout, &stderr, time.Now)
	if exit != 1 || !strings.Contains(stderr.String(), "--jsonl") {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
}
