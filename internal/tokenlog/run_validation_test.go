package tokenlog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/croutoncreations/redline/internal/tokenlog"
)

func TestRunUsageRecorderIgnoresNegativeHarnessCounters(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "cursor.jsonl")
	if err := os.WriteFile(artifact, []byte(`{"type":"result","usage":{"inputTokens":-1,"outputTokens":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	inserted, err := (tokenlog.RunUsageRecorder{Store: db}).RecordRunUsage(
		context.Background(),
		domain.Run{ID: "run-negative", ProviderAccountID: "cursor-main"},
		domain.ExecutionProfile{HarnessType: "cursor-agent", Model: "auto"},
		artifact,
		time.Now(),
	)
	if err != nil {
		t.Fatalf("malformed external usage must be ignored, not abort synchronization: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted = %d, want 0", inserted)
	}
}

func TestLoadRunArtifactIgnoresNegativeRawCountersBeforeNormalizing(t *testing.T) {
	cases := []struct{ name, harness, record string }{
		// input - cached would clamp to 0 and hide the malformed counter.
		{"codex negative input", "codex-cli", `{"type":"turn.completed","usage":{"input_tokens":-1,"cached_input_tokens":0,"output_tokens":5}}`},
		{"codex negative cached", "codex-cli", `{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":-3,"output_tokens":5}}`},
		// Alias selection prefers positive aliases and would skip a negative one.
		{"hermes negative alias", "hermes", `{"type":"hermes.result","model":"m","provider":"p","usage":{"input":-1,"input_tokens":4,"output":2}}`},
		{"hermes negative cache alias", "hermes", `{"type":"hermes.result","model":"m","provider":"p","usage":{"input":4,"output":2,"cached_input_tokens":-7}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.jsonl")
			if err := os.WriteFile(path, []byte(tc.record), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := tokenlog.LoadRunArtifact(path, tc.harness, "run-negative", "m", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("got %d observations, want malformed record ignored: %+v", len(got), got)
			}
		})
	}
}
