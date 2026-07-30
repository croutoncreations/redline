package activity_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jfox/redline/internal/activity"
	"github.com/jfox/redline/internal/domain"
)

func TestBuildPrefersStructuredResultAndFindsArtifacts(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "run.stdout.jsonl")
	if err := os.WriteFile(output, []byte(`{"type":"result","result":"fallback"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resultFile := filepath.Join(root, "run.result.json")
	if err := os.WriteFile(resultFile, []byte(`{
	  "summary":"Opened a focused fix.",
	  "outcome":"changes_proposed",
	  "artifacts":[{"type":"pull_request","label":"PR #42","url":"https://github.com/acme/app/pull/42"}],
	  "warnings":["One flaky test remains."],
	  "actual_provider":"anthropic-cli",
	  "actual_model":"claude-opus-4-1"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := activity.Build(activity.Input{
		State: domain.RunCompleted, OutputFile: output, ResultFile: resultFile,
		Workspace: domain.Workspace{Directory: "/tmp/work", Branch: "redline/fix"},
	})
	if got.Summary != "Opened a focused fix." || got.Outcome != "changes_proposed" ||
		got.ActualProvider != "anthropic-cli" || got.ActualModel != "claude-opus-4-1" ||
		len(got.Warnings) != 1 || len(got.Artifacts) != 3 {
		t.Fatalf("result = %#v", got)
	}
}

func TestBuildExtractsHarnessSummaryMetadataAndLinks(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "run.stdout.jsonl")
	body := `{"type":"item.completed","item":{"type":"agent_message","text":"Fixed the parser. Review https://github.com/acme/app/pull/51"}}` + "\n"
	if err := os.WriteFile(output, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := activity.Build(activity.Input{
		State: domain.RunCompleted, OutputFile: output,
		Metadata: map[string]any{"actual_provider": "openai-codex", "actual_model": "gpt-5.6-sol"},
	})
	if got.Summary != "Fixed the parser. Review https://github.com/acme/app/pull/51" ||
		got.ActualProvider != "openai-codex" || got.ActualModel != "gpt-5.6-sol" ||
		len(got.Artifacts) != 1 || got.Artifacts[0].Type != "pull_request" {
		t.Fatalf("result = %#v", got)
	}
}

func TestBuildMakesFailureHumanReadable(t *testing.T) {
	got := activity.Build(activity.Input{
		State: domain.RunFailed, Error: "Claude Code is signed out. Run `claude auth login`, then retry this job.",
	})
	if got.Outcome != "failed" || got.Summary == "" || len(got.Warnings) != 1 {
		t.Fatalf("result = %#v", got)
	}
}
