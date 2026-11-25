package harness_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/activity"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
	"github.com/croutoncreations/redline/internal/tokenlog"
)

// fakeAgent installs an `agent` executable on PATH that records how it was
// called and replays the recorded cursor-agent stream-json run. Nothing
// talks to Cursor.
func fakeAgent(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	bin := t.TempDir()
	path := filepath.Join(bin, "cursor-agent")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// End to end through the real process runner: the job prompt reaches the
// CLI on stdin, the CLI runs in the workspace with the configured model, and
// Redline turns the stream into a run summary and a token observation.
func TestCursorAgentRunEndToEndWithFakeCLI(t *testing.T) {
	stream, err := filepath.Abs(filepath.Join("..", "tokenlog", "testdata", "cursor-agent-stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "invocation")
	t.Setenv("CURSOR_API_KEY", "should-not-reach-the-cli")
	fakeAgent(t, `#!/bin/sh
{ printf 'cwd=%s\n' "$PWD"; printf 'args=%s\n' "$*"; printf 'key=%s\n' "${CURSOR_API_KEY:-unset}"; printf 'stdin='; cat; printf '\n'; } > "`+record+`"
cat "`+stream+`"
`)
	workspace := t.TempDir()
	output := t.TempDir()
	result, err := (harness.Adapter{}).Run(t.Context(), harness.Request{
		RunID: "run-e2e", OutputDirectory: output,
		Task:      domain.Task{ID: "task", Prompt: "Reply with exactly the word: ok"},
		Profile:   domain.ExecutionProfile{HarnessType: "cursor-agent", Model: "auto"},
		Workspace: domain.Workspace{Directory: workspace},
	})
	if err != nil || result.ExitCode != 0 || result.Failure != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	invocation, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cwd=" + workspace,
		"args=--print --output-format stream-json --trust --sandbox enabled --workspace " + workspace + " --model auto",
		"stdin=Reply with exactly the word: ok",
		// The CLI runs on its own login, never a key in Redline's environment.
		"key=unset",
	} {
		if !strings.Contains(string(invocation), want) {
			t.Fatalf("invocation missing %q:\n%s", want, invocation)
		}
	}
	summary := activity.Build(activity.Input{
		State: domain.RunCompleted, OutputFile: result.OutputFile, Provider: "cursor-agent", Model: "auto",
	})
	if summary.Summary != "ok" || summary.ActualModel != "auto" {
		t.Fatalf("activity = %#v", summary)
	}
	observations, err := tokenlog.LoadRunArtifact(result.OutputFile, "cursor-agent", "run-e2e", "auto", time.Now())
	if err != nil || len(observations) != 1 || observations[0].OutputTokens != 31 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
}

func TestCursorAgentSignedOutEndToEndWithFakeCLI(t *testing.T) {
	fakeAgent(t, `#!/bin/sh
cat > /dev/null
echo "Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY environment variable." >&2
exit 1
`)
	result, err := (harness.Adapter{}).Run(t.Context(), harness.Request{
		RunID: "run-e2e-signed-out", OutputDirectory: t.TempDir(),
		Task:      domain.Task{ID: "task", Prompt: "x"},
		Profile:   domain.ExecutionProfile{HarnessType: "cursor-agent", Model: "auto"},
		Workspace: domain.Workspace{Directory: t.TempDir()},
	})
	if err != nil || result.ExitCode != 1 || !harness.IsAuthenticationFailure(result.Failure) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

// Live smoke test against the signed-in Cursor CLI. It spends a tiny amount of
// Cursor subscription usage (Auto, from Cursor's own pool) and only runs when
// REDLINE_CURSOR_LIVE=1. It uses ask mode so the agent cannot change files.
func TestCursorAgentLiveSmoke(t *testing.T) {
	if os.Getenv("REDLINE_CURSOR_LIVE") != "1" {
		t.Skip("set REDLINE_CURSOR_LIVE=1 to run one real Cursor CLI job")
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Skip("Cursor CLI (agent) is not installed")
	}
	workspace := t.TempDir()
	result, err := (harness.Adapter{}).Run(t.Context(), harness.Request{
		RunID: "run-live", OutputDirectory: t.TempDir(),
		Task: domain.Task{ID: "task", Prompt: "Reply with exactly the word: redline-ok"},
		Profile: domain.ExecutionProfile{
			HarnessType: "cursor-agent", Model: "auto", HarnessArgs: []string{"--mode", "ask"},
		},
		Workspace: domain.Workspace{Directory: workspace},
	})
	if err != nil || result.ExitCode != 0 {
		stderr, _ := os.ReadFile(result.ErrorFile)
		t.Fatalf("result=%#v err=%v stderr=%s", result, err, stderr)
	}
	data, err := os.ReadFile(result.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	var terminal struct {
		Type, Subtype, Result string
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "result" || terminal.Subtype != "success" || !strings.Contains(terminal.Result, "redline-ok") {
		t.Fatalf("terminal = %#v", terminal)
	}
	entries, _ := os.ReadDir(workspace)
	if len(entries) != 0 {
		t.Fatalf("ask mode must not touch the workspace: %v", entries)
	}
	observations, err := tokenlog.LoadRunArtifact(result.OutputFile, "cursor-agent", "run-live", "auto", time.Now())
	if err != nil || len(observations) != 1 || observations[0].InputTokens == 0 {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
	t.Logf("live run ok: %d input, %d output tokens", observations[0].InputTokens, observations[0].OutputTokens)
}
