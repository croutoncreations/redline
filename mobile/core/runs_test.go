package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// runsServer serves a canned runs list and captures the request it received, so
// tests can assert both the decoding and the query the client actually sent.
func runsServer(t *testing.T, body string, status int) (*httptest.Server, *http.Request) {
	t.Helper()
	var captured *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		captured = clone
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func TestFetchRunsSummarisesForDisplay(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `[
	  {"id":"aaaa1111-2222-3333-4444-555566667777","task_id":"release-notes","state":"completed","outcome":"completed",
	   "started_at":"2026-09-02T11:00:00Z","completed_at":"2026-09-02T11:01:40Z","exit_code":0,
	   "summary":"Updated the changelog.",
	   "artifacts":[{"type":"pull_request","label":"Pull request","url":"https://example.com/pr/61"}]},
	  {"id":"bbbb1111-2222-3333-4444-555566667777","task_id":"nightly-tests","state":"failed","outcome":"failed",
	   "started_at":"2026-09-02T09:00:00Z","completed_at":"2026-09-02T09:30:00Z","exit_code":2,
	   "error":"tests failed"}
	]`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, err := client.FetchRuns()
	if err != nil {
		t.Fatalf("FetchRuns: %v", err)
	}

	var view RunListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if len(view.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(view.Runs))
	}

	first := view.Runs[0]
	if first.TaskID != "release-notes" {
		t.Errorf("task id = %q", first.TaskID)
	}
	// The full UUID is unusable on a phone; the UI needs something short.
	if first.ShortID != "aaaa1111" {
		t.Errorf("short id = %q, want aaaa1111", first.ShortID)
	}
	if !first.Succeeded {
		t.Error("a completed run with exit code 0 must read as succeeded")
	}
	if first.DurationLabel != "1m 40s" {
		t.Errorf("duration = %q, want 1m 40s", first.DurationLabel)
	}
	// "1h ago" is what a person wants; a timestamp makes them do the maths.
	if first.RelativeLabel != "1h ago" {
		t.Errorf("relative = %q, want 1h ago", first.RelativeLabel)
	}
	if first.PullRequestURL != "https://example.com/pr/61" {
		t.Errorf("pull request url = %q", first.PullRequestURL)
	}

	second := view.Runs[1]
	if second.Succeeded {
		t.Error("a failed run must not read as succeeded")
	}
	if second.Error != "tests failed" {
		t.Errorf("error = %q", second.Error)
	}
}

// A run that is still going has no completion time. Treating that as a zero
// duration would render "0s" for something that has been going for an hour.
func TestFetchRunsHandlesRunningRun(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `[{"id":"cccc1111-2222-3333-4444-555566667777","task_id":"long-job","state":"running",
	  "started_at":"2026-09-02T11:30:00Z"}]`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, err := client.FetchRuns()
	if err != nil {
		t.Fatalf("FetchRuns: %v", err)
	}
	var view RunListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	run := view.Runs[0]
	if !run.Running {
		t.Error("a run without a completion time must read as running")
	}
	if run.DurationLabel != "30m" {
		t.Errorf("running duration should count up to now, got %q", run.DurationLabel)
	}
	if run.Succeeded {
		t.Error("a running run has not succeeded yet")
	}
}

// The whole point of the runs screen is spotting failures, so the summary has
// to be trustworthy.
func TestRunListViewCountsFailures(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	body := `[
	  {"id":"a1111111","task_id":"t","state":"completed","outcome":"completed","started_at":"2026-09-02T11:00:00Z","completed_at":"2026-09-02T11:01:00Z"},
	  {"id":"b2222222","task_id":"t","state":"failed","outcome":"failed","started_at":"2026-09-02T10:00:00Z","completed_at":"2026-09-02T10:01:00Z"},
	  {"id":"c3333333","task_id":"t","state":"failed","outcome":"failed","started_at":"2026-09-02T09:00:00Z","completed_at":"2026-09-02T09:01:00Z"}
	]`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, _ := client.FetchRuns()
	var view RunListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.FailedCount != 2 {
		t.Errorf("failed count = %d, want 2", view.FailedCount)
	}
	if view.TotalCount != 3 {
		t.Errorf("total = %d, want 3", view.TotalCount)
	}
}

func TestFetchRunsRejectsUnauthorized(t *testing.T) {
	server, _ := runsServer(t, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	client := NewClient(server.URL, "bad")
	_, err := client.FetchRuns()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsUnauthorized(err) {
		t.Errorf("401 must be reported as unauthorized, got %v", err)
	}
}

func TestFetchRunLogsReturnsContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("stream"); got != "stdout" {
			t.Errorf("stream = %q, want stdout", got)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/runs/run-1/logs") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"content":"line one\nline two\n"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
	if !strings.Contains(logs, "line two") {
		t.Errorf("logs = %q", logs)
	}
}

// An empty stream must select stdout rather than sending stream= and getting a
// 400 back from a service that validates the parameter.
func TestFetchRunLogsDefaultsToStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("stream"); got != "stdout" {
			t.Errorf("stream = %q, want stdout", got)
		}
		_, _ = w.Write([]byte(`{"content":"x"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	if _, err := client.FetchRunLogs("run-1", ""); err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
}

// 202 means a run actually started. This is the only status that should tell
// the user something is now happening.
func TestDispatchTaskReportsStartedRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		// The service rejects any body on this endpoint, so the client must
		// not send one.
		body := make([]byte, 1)
		if n, _ := r.Body.Read(body); n != 0 {
			t.Error("dispatch must not send a request body")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"run":{"id":"dddd1111-2222","task_id":"nightly"},
		  "result":{"decision":"dispatch","mode":"window_slots","reason":"capacity available"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	raw, err := client.DispatchTask("nightly")
	if err != nil {
		t.Fatalf("DispatchTask: %v", err)
	}
	var result DispatchView
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Started {
		t.Error("202 must report as started")
	}
	if result.RunID != "dddd1111-2222" {
		t.Errorf("run id = %q", result.RunID)
	}
	if result.Reason != "capacity available" {
		t.Errorf("reason = %q", result.Reason)
	}
}

// 200 means the scheduler considered the request and declined to start
// anything. Reporting that as success would be a lie the user acts on.
func TestDispatchTaskReportsHeldBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{"decision":"wait","mode":"pace_threshold",
		  "reason":"weekly pace would be exceeded"}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	raw, err := client.DispatchTask("nightly")
	if err != nil {
		t.Fatalf("DispatchTask should not error on a considered refusal: %v", err)
	}
	var result DispatchView
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Started {
		t.Error("200 means nothing started")
	}
	if result.Reason != "weekly pace would be exceeded" {
		t.Errorf("the scheduler's reason must survive to the UI, got %q", result.Reason)
	}
}

// 409 is a refusal with a reason the user can act on, so it must not surface as
// a generic failure.
func TestDispatchTaskExplainsConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"task is disabled"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	raw, err := client.DispatchTask("nightly")
	if err != nil {
		t.Fatalf("a refusal is an answer, not a transport error: %v", err)
	}
	var result DispatchView
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Started {
		t.Error("a conflict did not start anything")
	}
	if result.Refused != true {
		t.Error("a conflict must be marked as refused")
	}
	if result.Reason != "task is disabled" {
		t.Errorf("reason = %q, want the service's explanation", result.Reason)
	}
}

func TestFetchTasksListsDispatchableTasks(t *testing.T) {
	body := `[
	  {"id":"nightly","name":"Nightly tests","enabled":true,"state":"queued","type":"recurring"},
	  {"id":"old","name":"Retired","enabled":false,"state":"disabled","type":"one_off"}
	]`
	server, _ := runsServer(t, body, http.StatusOK)

	client := NewClient(server.URL, "token")
	raw, err := client.FetchTasks()
	if err != nil {
		t.Fatalf("FetchTasks: %v", err)
	}
	var view TaskListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Tasks) != 2 {
		t.Fatalf("expected both tasks, got %d", len(view.Tasks))
	}
	// Only a queued, enabled task can be dispatched; offering the button for
	// the others would produce a guaranteed 409.
	if !view.Tasks[0].Dispatchable {
		t.Error("an enabled queued task must be dispatchable")
	}
	if view.Tasks[1].Dispatchable {
		t.Error("a disabled task must not be dispatchable")
	}
}

// Harness logs are JSONL of an agent transcript. Showing the raw lines puts a
// wall of escaped JSON on a phone screen, where the useful content is a few
// sentences and the names of the tools that ran.
func TestFetchRunLogsRendersAgentTranscript(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"system","subtype":"task_started","task":"flaky-tests"}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the failing test."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"The timeout was too short."}]}}`,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": jsonl})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}

	// The assistant's own words are the point.
	if !strings.Contains(logs, "Looking at the failing test.") {
		t.Errorf("assistant text missing from:\n%s", logs)
	}
	if !strings.Contains(logs, "The timeout was too short.") {
		t.Errorf("later assistant text missing from:\n%s", logs)
	}
	// Knowing a command ran matters; the full arguments do not.
	if !strings.Contains(logs, "Bash") {
		t.Errorf("tool use missing from:\n%s", logs)
	}
	// Token accounting is noise on a phone.
	if strings.Contains(logs, "thinking_tokens") {
		t.Errorf("bookkeeping should be filtered out of:\n%s", logs)
	}
	// No raw JSON should survive.
	if strings.Contains(logs, `"type":"assistant"`) {
		t.Errorf("raw JSON leaked into:\n%s", logs)
	}
}

// Not every log is an agent transcript: plain output such as test results must
// pass through untouched rather than being swallowed by the parser.
func TestFetchRunLogsPassesThroughPlainText(t *testing.T) {
	plain := "ok  \tgithub.com/jfox/redline/internal/store\t66.589s\nFAIL\tinternal/api\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": plain})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
	if !strings.Contains(logs, "FAIL\tinternal/api") {
		t.Errorf("plain output must survive, got:\n%s", logs)
	}
}

// The logs endpoint returns the tail of a file, so the first line is often a
// fragment cut mid-JSON. Passing that through prints a wall of escaped JSON as
// the very first thing on screen, which is what a reader sees first.
func TestFetchRunLogsDropsATruncatedLeadingFragment(t *testing.T) {
	// A fragment that starts mid-string, then a clean transcript line.
	jsonl := `k  \tgithub.com/x/y\t3.801s","is_error":false}]},"session_id":"abc"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Real content."}]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": jsonl})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}

	if !strings.Contains(logs, "Real content.") {
		t.Errorf("the intact line must survive, got:\n%s", logs)
	}
	if strings.Contains(logs, `"is_error"`) {
		t.Errorf("a truncated JSON fragment must not be shown, got:\n%s", logs)
	}
}

// A log viewer must never claim there is no output when there is. Reducing a
// transcript to nothing is worse than showing noise: "No output" reads as
// "nothing happened" rather than "we hid it all".
func TestFetchRunLogsNeverBlanksNonEmptyOutput(t *testing.T) {
	// A transcript made entirely of entries the renderer treats as noise.
	jsonl := strings.Join([]string{
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}`,
		`{"type":"tool_progress","status":"running"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`,
	}, "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": jsonl})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
	if strings.TrimSpace(logs) == "" {
		t.Fatal("output that exists must never render as nothing")
	}
}

// Ordinary output that happens to be JSON is not a transcript. Discarding it
// because it parsed would delete exactly the report someone is looking for.
func TestFetchRunLogsKeepsUnrelatedJSON(t *testing.T) {
	jsonl := `{"failures":3,"suite":"integration"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Investigating."}]}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": jsonl})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
	if !strings.Contains(logs, `"failures":3`) {
		t.Errorf("unrelated JSON must survive, got:\n%s", logs)
	}
	if !strings.Contains(logs, "Investigating.") {
		t.Errorf("transcript prose must still render, got:\n%s", logs)
	}
}

// The final error of a failing run often arrives as a result entry. Dropping
// unrecognised entry types would hide the one line that matters.
func TestFetchRunLogsSurfacesResultEntries(t *testing.T) {
	jsonl := `{"type":"result","subtype":"error","result":"build failed: undefined symbol"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := json.Marshal(map[string]string{"content": jsonl})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	logs, err := client.FetchRunLogs("run-1", "stdout")
	if err != nil {
		t.Fatalf("FetchRunLogs: %v", err)
	}
	if !strings.Contains(logs, "build failed: undefined symbol") {
		t.Errorf("a result entry carries the failure reason, got:\n%s", logs)
	}
}

// A dispatch for a task that does not exist is not a transport failure. Saying
// "check the desktop is awake" when the desktop answered is the same class of
// lie the 200/202 split exists to avoid.
func TestDispatchTaskExplainsUnknownTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"task not found"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	raw, err := client.DispatchTask("ghost")
	if err != nil {
		t.Fatalf("a 404 is an answer, not a transport error: %v", err)
	}
	var result DispatchView
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Started {
		t.Error("nothing started")
	}
	if !result.Refused {
		t.Error("an unknown task must read as refused")
	}
	if result.Reason != "task not found" {
		t.Errorf("reason = %q, want the service's explanation", result.Reason)
	}
}

// A 401 must stay an auth error so the UI can tell the user to pair again,
// rather than being folded into the refusal path.
func TestDispatchTaskStillReportsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "bad")
	if _, err := client.DispatchTask("nightly"); !IsUnauthorized(err) {
		t.Errorf("401 must remain unauthorized, got %v", err)
	}
}

// A 202 with no body still means a run started. Treating the empty body as a
// decode failure would report a successful dispatch as an error.
func TestDispatchTaskAcceptsEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewClient(server.URL, "token")
	raw, err := client.DispatchTask("nightly")
	if err != nil {
		t.Fatalf("an empty 202 body must not be an error: %v", err)
	}
	var result DispatchView
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.Started {
		t.Error("202 means a run started, body or no body")
	}
}

// Past a couple of days, hours stop being a unit anyone reads: "317h 20m" is
// arithmetic homework where "13 days" is an answer.
func TestDurationLabelUsesDaysForLongWaits(t *testing.T) {
	for _, testCase := range []struct {
		elapsed time.Duration
		want    string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m 30s"},
		{2 * time.Hour, "2h"},
		{time.Duration(4.5 * float64(time.Hour)), "4h 30m"},
		{47 * time.Hour, "47h"},
		{49 * time.Hour, "2d"},
		{317*time.Hour + 20*time.Minute, "13d"},
	} {
		if got := durationLabel(testCase.elapsed); got != testCase.want {
			t.Errorf("durationLabel(%s) = %q, want %q", testCase.elapsed, got, testCase.want)
		}
	}
}

// The run record carries only a task id. The web dashboard shows the task's
// human name, which is what someone recognises at a glance, so the core joins
// the two rather than making each platform do it.
func TestFetchRunsUsesTaskNamesWhenAvailable(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/runs"):
			_, _ = w.Write([]byte(`[{"id":"a1-b2","task_id":"release-notes","state":"completed",
			  "outcome":"completed","started_at":"2026-09-02T11:00:00Z",
			  "completed_at":"2026-09-02T11:01:00Z",
			  "actual_provider":"claude-code","actual_model":"sonnet"}]`))
		case r.URL.Path == "/v1/tasks":
			_, _ = w.Write([]byte(`[{"id":"release-notes","name":"Draft changelog and release notes",
			  "enabled":true,"state":"queued"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, err := client.FetchRuns()
	if err != nil {
		t.Fatalf("FetchRuns: %v", err)
	}
	var view RunListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	run := view.Runs[0]
	if run.Name != "Draft changelog and release notes" {
		t.Errorf("name = %q, want the task's human name", run.Name)
	}
	// The id stays available: it is what identifies the task in the API.
	if run.TaskID != "release-notes" {
		t.Errorf("task id = %q", run.TaskID)
	}
	// "claude-code · sonnet" tells you which harness and model actually ran,
	// which matters when one is misbehaving.
	if run.MetaLabel != "claude-code \u00b7 sonnet" {
		t.Errorf("meta = %q", run.MetaLabel)
	}
}

// A run whose task has since been deleted must still render, falling back to
// the id rather than showing a blank row.
func TestFetchRunsFallsBackToTaskIDWhenTheTaskIsGone(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/runs"):
			_, _ = w.Write([]byte(`[{"id":"a1-b2","task_id":"deleted-task","state":"completed",
			  "outcome":"completed","started_at":"2026-09-02T11:00:00Z",
			  "completed_at":"2026-09-02T11:01:00Z"}]`))
		case r.URL.Path == "/v1/tasks":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, _ := client.FetchRuns()
	var view RunListView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Runs[0].Name != "deleted-task" {
		t.Errorf("name = %q, want the id as a fallback", view.Runs[0].Name)
	}
}

// Task lookup is a nicety. If it fails, the runs list must still render with
// ids rather than failing entirely: the runs are the point, the names are
// decoration.
func TestFetchRunsSurvivesATaskLookupFailure(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/runs") {
			_, _ = w.Write([]byte(`[{"id":"a1-b2","task_id":"t","state":"completed",
			  "outcome":"completed","started_at":"2026-09-02T11:00:00Z",
			  "completed_at":"2026-09-02T11:01:00Z"}]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithClock(server.URL, "token", fixedClock(now))
	raw, err := client.FetchRuns()
	if err != nil {
		t.Fatalf("a task lookup failure must not fail the runs list: %v", err)
	}
	var view RunListView
	_ = json.Unmarshal([]byte(raw), &view)
	if len(view.Runs) != 1 || view.Runs[0].Name != "t" {
		t.Errorf("runs must still render, got %+v", view.Runs)
	}
}
