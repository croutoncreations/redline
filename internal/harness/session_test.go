package harness_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

// The usage-limit lines follow the stream-json shapes Claude Code 2.1.318
// emits when the account's plan limit refuses a request: an assistant event
// with error "rate_limit" and api_error "usage_limit_reached", and a final
// error result.
const claudeUsageLimitedStream = `{"type":"system","subtype":"init","session_id":"sess-123","cwd":"/tmp/work"}
{"type":"assistant","session_id":"sess-123","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","session_id":"sess-123","message":{"content":[{"type":"tool_result","content":"usage limit reached in fixture text"}]}}
{"type":"assistant","session_id":"sess-123","error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":{"status":"rejected","resetsAt":1800000000,"rateLimitType":"five_hour"}},"message":{"content":[{"type":"text","text":"You've hit your session limit · resets 3am"}]},"is_api_error_message":true}
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error","session_id":"sess-123","result":"You've hit your session limit · resets 3am"}
`

type scriptedRunner struct {
	stdout   string
	stderr   string
	exitCode int
	command  redprocess.Command
	stdin    string
}

func (r *scriptedRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	if command.Stdin != nil {
		data, _ := io.ReadAll(command.Stdin)
		r.stdin = string(data)
	}
	_, _ = io.WriteString(command.Stdout, r.stdout)
	_, _ = io.WriteString(command.Stderr, r.stderr)
	return r.exitCode, nil
}

func claudeRequest(t *testing.T, runID string) harness.Request {
	return harness.Request{
		RunID: runID, OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "fix the flaky test"},
		Profile:   domain.ExecutionProfile{HarnessType: "claude-code", Model: "sonnet"},
		Workspace: domain.Workspace{Directory: "/tmp/work"},
	}
}

func TestClaudeAdapterRecognisesUsageLimitAndRecordsSession(t *testing.T) {
	runner := &scriptedRunner{stdout: claudeUsageLimitedStream, exitCode: 1}
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-limited"))
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "sess-123" {
		t.Fatalf("session id = %q", result.SessionID)
	}
	if result.UsageLimit == nil {
		t.Fatal("usage limit was not recognised")
	}
	if want := time.Unix(1800000000, 0).UTC(); !result.UsageLimit.ResetsAt.Equal(want) ||
		result.UsageLimit.LimitType != "five_hour" {
		t.Fatalf("usage limit = %#v", result.UsageLimit)
	}
	if !strings.HasPrefix(result.Failure, "Claude Code reached the account's usage limit (five hour)") ||
		!strings.Contains(result.Failure, "2027-01-15T08:00:00Z") {
		t.Fatalf("failure = %q", result.Failure)
	}
	if harness.IsAuthenticationFailure(result.Failure) {
		t.Fatal("a usage limit must not pause the account as signed out")
	}
}

func TestClaudeAdapterRecognisesRejectedRateLimitEvent(t *testing.T) {
	runner := &scriptedRunner{exitCode: 1, stdout: `{"type":"system","subtype":"init","session_id":"sess-9"}
{"type":"rate_limit_event","session_id":"sess-9","rate_limit_info":{"status":"rejected","resetsAt":1800003600,"rateLimitType":"seven_day"}}
{"type":"result","subtype":"success","is_error":true,"session_id":"sess-9"}
`}
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-rejected"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageLimit == nil || result.UsageLimit.LimitType != "seven_day" ||
		!result.UsageLimit.ResetsAt.Equal(time.Unix(1800003600, 0).UTC()) {
		t.Fatalf("usage limit = %#v", result.UsageLimit)
	}
}

func TestClaudeAdapterDoesNotTreatOrdinaryFailuresAsUsageLimits(t *testing.T) {
	for name, stdout := range map[string]string{
		// Allowed-status rate limit events are routine progress updates.
		"allowed rate limit event": `{"type":"rate_limit_event","session_id":"s","rate_limit_info":{"status":"allowed_warning","resetsAt":1800000000,"rateLimitType":"seven_day"}}
{"type":"result","is_error":true,"session_id":"s","result":"tests failed"}`,
		// Tool output that merely mentions a limit is task content.
		"tool output mentioning a limit": `{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result","content":"{\"api_error\":\"usage_limit_reached\"} You've hit your session limit"}]}}
{"type":"result","is_error":true,"session_id":"s","result":"done"}`,
		// A transient throttle without the plan-limit marker is retried by
		// Claude Code itself; if it still fails, that is an ordinary failure.
		"plain rate limit error": `{"type":"assistant","session_id":"s","error":"rate_limit","message":{"content":[{"type":"text","text":"API Error: Request rejected (429)"}]}}`,
		// Claude Code got past an earlier refusal (e.g. on another model);
		// the run then failed for its own reasons.
		"refusal followed by more work": `{"type":"assistant","session_id":"s","error":"rate_limit","api_error":"usage_limit_reached","api_error_params":{"rate_limit_info":{"status":"rejected","resetsAt":1800000000,"rateLimitType":"seven_day_opus"}}}
{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"Switching models and continuing."}]}}
{"type":"result","is_error":true,"session_id":"s","result":"tests failed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := &scriptedRunner{stdout: stdout, exitCode: 1}
			result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-ordinary"))
			if err != nil {
				t.Fatal(err)
			}
			if result.UsageLimit != nil || strings.Contains(result.Failure, "usage limit") {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestUsageLimitIsOnlyReadFromFailedRuns(t *testing.T) {
	runner := &scriptedRunner{stdout: claudeUsageLimitedStream, exitCode: 0}
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-ok"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageLimit != nil || result.Failure != "" || result.SessionID != "sess-123" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClaudeAdapterResumesSessionWithContinuationPrompt(t *testing.T) {
	runner := &scriptedRunner{stdout: `{"type":"system","subtype":"init","session_id":"sess-123"}`}
	request := claudeRequest(t, "run-resume")
	request.ResumeSessionID = "sess-123"
	request.Profile.HarnessArgs = []string{"--effort", "high"}
	if _, err := (harness.Adapter{Runner: runner}).Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	want := "--print --output-format stream-json --verbose --permission-mode auto --resume sess-123 --model sonnet --effort high --append-system-prompt"
	if got := strings.Join(runner.command.Args, " "); !strings.HasPrefix(got, want) {
		t.Fatalf("args = %q", got)
	}
	if runner.stdin != harness.ResumePrompt || strings.Contains(runner.stdin, "flaky") {
		t.Fatalf("stdin = %q", runner.stdin)
	}
}

func TestResumeIsRefusedForHarnessesThatCannotResume(t *testing.T) {
	runner := &scriptedRunner{}
	request := claudeRequest(t, "run-codex")
	request.Profile = domain.ExecutionProfile{HarnessType: "codex-cli"}
	request.ResumeSessionID = "sess-123"
	if _, err := (harness.Adapter{Runner: runner}).Run(t.Context(), request); err == nil || runner.command.Name != "" {
		t.Fatalf("err=%v command=%#v", err, runner.command)
	}
}

func TestClaudeAdapterReportsRejectedResume(t *testing.T) {
	runner := &scriptedRunner{exitCode: 1, stderr: "No conversation found with session ID: sess-gone\n"}
	request := claudeRequest(t, "run-gone")
	request.ResumeSessionID = "sess-gone"
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ResumeRejected || result.UsageLimit != nil {
		t.Fatalf("result = %#v", result)
	}
	for name, runner := range map[string]*scriptedRunner{
		// Not a resume: the same text is just output.
		"fresh run": {exitCode: 1, stderr: "No conversation found with session ID: x\n"},
	} {
		result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-"+name))
		if err != nil || result.ResumeRejected {
			t.Fatalf("%s: result=%#v err=%v", name, result, err)
		}
	}
	// The session opened (stream events on stdout); the text came from a
	// tool, and the run failed for its own reasons.
	runner = &scriptedRunner{exitCode: 1,
		stdout: `{"type":"system","subtype":"init","session_id":"sess-gone"}` + "\n",
		stderr: "No conversation found with session ID: from a test fixture\n"}
	result, err = (harness.Adapter{Runner: runner}).Run(t.Context(), request)
	if err != nil || result.ResumeRejected {
		t.Fatalf("opened session: result=%#v err=%v", result, err)
	}
}

func TestUsageLimitWinsOverSignOutTextInTaskOutput(t *testing.T) {
	stdout := `{"type":"user","session_id":"s","message":{"content":[{"type":"tool_result","content":"gh: not logged in"}]}}` + "\n" + claudeUsageLimitedStream
	runner := &scriptedRunner{stdout: stdout, exitCode: 1}
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), claudeRequest(t, "run-both"))
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageLimit == nil || harness.IsAuthenticationFailure(result.Failure) {
		t.Fatalf("result = %#v", result)
	}
}

func TestOnlyClaudeRunsRecordSessions(t *testing.T) {
	runner := &scriptedRunner{stdout: `{"type":"session","version":3,"id":"pi-session-1","session_id":"x"}`}
	request := claudeRequest(t, "run-pi")
	request.Profile = domain.ExecutionProfile{HarnessType: "pi"}
	result, err := (harness.Adapter{Runner: runner}).Run(t.Context(), request)
	if err != nil || result.SessionID != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestProfileCanResume(t *testing.T) {
	for _, test := range []struct {
		profile domain.ExecutionProfile
		want    bool
	}{
		{domain.ExecutionProfile{HarnessType: "claude-code"}, true},
		{domain.ExecutionProfile{HarnessType: "claude-code", HarnessArgs: []string{"--no-session-persistence"}}, false},
		{domain.ExecutionProfile{HarnessType: "pi"}, false},
		{domain.ExecutionProfile{HarnessType: "codex-cli"}, false},
	} {
		if got := harness.ProfileCanResume(test.profile); got != test.want {
			t.Errorf("ProfileCanResume(%#v) = %v, want %v", test.profile, got, test.want)
		}
	}
}
