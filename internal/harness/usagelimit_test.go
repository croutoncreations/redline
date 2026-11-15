package harness_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

const claudeLimitedTranscript = `{"type":"system","subtype":"init","cwd":"/tmp/work","session_id":"sess-claude"}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1794546600,"rateLimitType":"five_hour","overageStatus":"rejected","isUsingOverage":false},"session_id":"sess-claude"}
{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 10am (America/New_York)","session_id":"sess-claude"}
`

func writeTranscript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectUsageLimitReadsClaudeRejectionAndResult(t *testing.T) {
	limit := harness.DetectUsageLimit("claude-code", writeTranscript(t, claudeLimitedTranscript))
	if limit == nil {
		t.Fatal("usage limit was not detected")
	}
	if limit.SessionID != "sess-claude" || limit.LimitType != "five_hour" ||
		limit.ResetsAt == nil || limit.ResetsAt.Unix() != 1794546600 ||
		!strings.Contains(limit.Message, "hit your limit") {
		t.Fatalf("limit = %#v", limit)
	}
}

func TestDetectUsageLimitReadsCodexAndPiErrors(t *testing.T) {
	codex := `{"type":"thread.started","thread_id":"thread-1"}
{"type":"turn.started"}
{"type":"error","message":"You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at 3:05 PM."}
{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}
`
	limit := harness.DetectUsageLimit("codex-cli", writeTranscript(t, codex))
	if limit == nil || limit.SessionID != "thread-1" || !strings.Contains(limit.Message, "usage limit") {
		t.Fatalf("codex limit = %#v", limit)
	}
	pi := `{"type":"session","version":3,"id":"pi-session","cwd":"/tmp/work"}
{"type":"agent_start"}
{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your account's rate limit.\"}}"}}
`
	limit = harness.DetectUsageLimit("pi", writeTranscript(t, pi))
	if limit == nil || limit.SessionID != "pi-session" {
		t.Fatalf("pi limit = %#v", limit)
	}
}

// A task's own output can mention limits; only the CLI's error fields count.
func TestDetectUsageLimitIgnoresToolOutputAndAllowedEvents(t *testing.T) {
	for name, transcript := range map[string]string{
		"allowed warning": `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","rateLimitType":"five_hour"}}
{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
		"tool output": `{"type":"user","message":{"content":[{"type":"tool_result","content":"Error: You've hit your usage limit","is_error":true}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Fixed the usage limit check."}`,
		"other error": `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`,
	} {
		if limit := harness.DetectUsageLimit("claude-code", writeTranscript(t, transcript)); limit != nil {
			t.Errorf("%s: misclassified as usage limit: %#v", name, limit)
		}
	}
	codexTool := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"You've hit your usage limit"}}`
	if limit := harness.DetectUsageLimit("codex-cli", writeTranscript(t, codexTool)); limit != nil {
		t.Errorf("codex tool output misclassified: %#v", limit)
	}
	if limit := harness.DetectUsageLimit("command", writeTranscript(t, claudeLimitedTranscript)); limit != nil {
		t.Errorf("custom command harness misclassified: %#v", limit)
	}
}

type transcriptRunner struct {
	transcript string
	exitCode   int
}

func (r transcriptRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	_, _ = io.WriteString(command.Stdout, r.transcript)
	return r.exitCode, nil
}

// Claude Code can end a limited run with exit code 0; it is still a failure.
func TestAdapterReportsUsageLimitEvenWhenClaudeExitsZero(t *testing.T) {
	result, err := (harness.Adapter{Runner: transcriptRunner{transcript: claudeLimitedTranscript}}).Run(context.Background(), harness.Request{
		RunID: "run", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "work"},
		Profile:   domain.ExecutionProfile{HarnessType: "claude-code"},
		Workspace: domain.Workspace{Directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UsageLimit == nil || !strings.HasPrefix(result.Failure, "Claude Code stopped at its usage limit.") ||
		harness.IsAuthenticationFailure(result.Failure) {
		t.Fatalf("result = %#v", result)
	}
}

// Only how the run ended counts: a limit the CLI recovered from is not a stop.
func TestDetectUsageLimitIgnoresLimitsTheRunRecoveredFrom(t *testing.T) {
	cases := map[string]struct{ harness, transcript string }{
		"claude rejected then success": {"claude-code", `{"type":"system","subtype":"init","session_id":"s"}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1794546600,"rateLimitType":"five_hour"}}
{"type":"result","subtype":"success","is_error":false,"result":"done"}`},
		"claude rejected but using overage": {"claude-code", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","overageStatus":"allowed","isUsingOverage":true}}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 500 Internal server error"}`},
		"claude rejected then window reopened": {"claude-code", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 500 Internal server error"}`},
		"codex error then completed turn": {"codex-cli", `{"type":"thread.started","thread_id":"t"}
{"type":"error","message":"You've hit your usage limit."}
{"type":"turn.started"}
{"type":"turn.completed","usage":{}}`},
		"pi limit then successful message": {"pi", `{"type":"session","id":"p"}
{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"You've hit your usage limit."}}
{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`},
		"pi organization 429": {"pi", `{"type":"session","id":"p"}
{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of request tokens has exceeded your per-minute rate limit\"}}"}}`},
	}
	for name, c := range cases {
		if limit := harness.DetectUsageLimit(c.harness, writeTranscript(t, c.transcript)); limit != nil {
			t.Errorf("%s: misclassified as usage limit: %#v", name, limit)
		}
	}
}

func TestDetectUsageLimitReadsLegacyClaudeResetTime(t *testing.T) {
	transcript := `{"type":"system","subtype":"init","session_id":"s"}
{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|1794546600"}`
	limit := harness.DetectUsageLimit("claude-code", writeTranscript(t, transcript))
	if limit == nil || limit.ResetsAt == nil || limit.ResetsAt.Unix() != 1794546600 || limit.SessionID != "s" {
		t.Fatalf("limit = %#v", limit)
	}
}

// pi ends with turn_end/agent_end events that repeat every message; on a
// long run they can be far larger than any tail window.
func TestDetectUsageLimitFindsPiErrorBeforeAHugeAgentEnd(t *testing.T) {
	huge := `{"type":"agent_end","messages":[{"role":"toolResult","content":"` + strings.Repeat("x", 600*1024) + `"}]}`
	transcript := `{"type":"session","id":"pi-session"}
{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"You've hit your usage limit."}}
` + huge + "\n" + `{"type":"agent_settled"}` + "\n"
	limit := harness.DetectUsageLimit("pi", writeTranscript(t, transcript))
	if limit == nil || limit.SessionID != "pi-session" {
		t.Fatalf("limit = %#v", limit)
	}
}

func TestDetectUsageLimitFollowsTheLatestRateLimitStateAndFinalCodexFailure(t *testing.T) {
	claude := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","overageStatus":"allowed","isUsingOverage":true}}
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 500 Internal server error"}`
	if limit := harness.DetectUsageLimit("claude-code", writeTranscript(t, claude)); limit != nil {
		t.Errorf("overage took over, but a usage limit was reported: %#v", limit)
	}
	codex := `{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"error","message":"You've hit your usage limit."}
{"type":"turn.failed","error":{"message":"Not logged in: login required"}}`
	if limit := harness.DetectUsageLimit("codex-cli", writeTranscript(t, codex)); limit != nil {
		t.Errorf("a final sign-out was reported as a usage limit: %#v", limit)
	}
}
