package tokenlog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/tokenlog"
)

func TestLoadRunArtifactReadsClaudeResultWithoutDoubleCountingMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	data := `{"type":"assistant","message":{"model":"claude-haiku-4-5","usage":{"input_tokens":3620,"output_tokens":4}}}
{"type":"result","usage":{"input_tokens":3620,"output_tokens":73,"cache_read_input_tokens":11,"cache_creation_input_tokens":7},"modelUsage":{"claude-haiku-4-5":{"inputTokens":3620,"outputTokens":73,"cacheReadInputTokens":11,"cacheCreationInputTokens":7}}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 7, 21, 17, 4, 53, 0, time.UTC)
	got, err := tokenlog.LoadRunArtifact(path, "claude-code", "run-1", "haiku", observedAt)
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	item := got[0]
	if item.Provider != "claude" || item.Source != "redline-run" || item.SourceID != "run-1:claude-haiku-4-5" ||
		item.Model != "claude-haiku-4-5" || item.InputTokens != 3620 || item.OutputTokens != 73 ||
		item.CacheReadTokens != 11 || item.CacheCreationTokens != 7 || item.Confidence != "high" {
		t.Fatalf("observation=%#v", item)
	}
}

func TestRunUsageRecorderPersistsOwnedRunExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"result","usage":{"input_tokens":12,"output_tokens":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := tokenlog.RunUsageRecorder{Store: db}
	run := domain.Run{ID: "owned-run"}
	profile := domain.ExecutionProfile{HarnessType: "claude-code", Model: "haiku"}
	for range 2 {
		if _, err := recorder.RecordRunUsage(context.Background(), run, profile, path, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListTokenObservations(t.Context(), "claude", time.Time{}, time.Time{})
	if err != nil || len(got) != 1 || got[0].Source != "redline-run" || got[0].SourceID != "owned-run" {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
}

func TestLoadRunArtifactReadsCodexTurnUsageAndSeparatesCachedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.jsonl")
	data := `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":12}}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenlog.LoadRunArtifact(path, "codex-cli", "run-2", "gpt-5.6-sol", time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	item := got[0]
	if item.Provider != "codex" || item.SourceID != "run-2" || item.Model != "gpt-5.6-sol" ||
		item.InputTokens != 60 || item.CacheReadTokens != 40 || item.OutputTokens != 12 {
		t.Fatalf("observation=%#v", item)
	}
}

func TestLoadRunArtifactReadsHermesUsageAndMapsSubscriptionProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes.jsonl")
	data := `{"type":"hermes.result","model":"gpt-5.5","provider":"openai-codex","usage":{"input":100,"output":12,"cache_read":40}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-hermes", "openai-codex/gpt-5.5", time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	item := got[0]
	if item.Provider != "codex" || item.SourceID != "run-hermes" || item.Model != "gpt-5.5" ||
		item.InputTokens != 100 || item.CacheReadTokens != 40 || item.OutputTokens != 12 {
		t.Fatalf("observation=%#v", item)
	}
}

func TestLoadRunArtifactReadsExistingHermesJobUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-job.jsonl")
	data := `{"type":"hermes.result","job_id":"job-seo-planner","session_id":"cron_job-seo-planner_new","model":"claude-fable-5-medium","provider":"custom:cliproxyapi-plus","usage":{"input":120,"output":8,"cache_read":400,"cache_write":5}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-hermes-job", "custom:cliproxyapi-plus/claude-fable-5-medium", time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	item := got[0]
	if item.Provider != "claude" || item.Model != "claude-fable-5-medium" ||
		item.InputTokens != 120 || item.OutputTokens != 8 ||
		item.CacheReadTokens != 400 || item.CacheCreationTokens != 5 {
		t.Fatalf("observation=%#v", item)
	}
}
