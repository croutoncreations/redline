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

// TestNormalizeHermesProviderDirectAnthropicNames verifies that the literal
// provider strings "anthropic" and "anthropic-cli" both map to "claude".
// Bug class: if these cases fall through to the default branch, tokens would be
// attributed to the raw provider string and go uncounted toward Claude quota.
func TestNormalizeHermesProviderDirectAnthropicNames(t *testing.T) {
	for _, provider := range []string{"anthropic", "anthropic-cli"} {
		path := filepath.Join(t.TempDir(), "hermes.jsonl")
		data := `{"type":"hermes.result","model":"claude-opus-4","provider":"` + provider + `","usage":{"input":50,"output":10}}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-anthr", "claude-opus-4", time.Now())
		if err != nil || len(got) != 1 {
			t.Fatalf("provider=%q: observations=%#v err=%v", provider, got, err)
		}
		if got[0].Provider != "claude" {
			t.Errorf("provider=%q: got Provider=%q, want \"claude\"", provider, got[0].Provider)
		}
	}
}

// TestNormalizeHermesProviderCustomPrefixGptMapsToCodex verifies that a
// custom-prefixed provider with a GPT or codex model maps to "codex".
// Bug class: without this branch, usage from OpenAI-compatible proxies would
// accumulate in an arbitrary provider bucket and be invisible to the codex
// quota check.
func TestNormalizeHermesProviderCustomPrefixGptMapsToCodex(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-4.5-preview", "o3-codex"} {
		path := filepath.Join(t.TempDir(), "hermes.jsonl")
		data := `{"type":"hermes.result","model":"` + model + `","provider":"custom:myproxy","usage":{"input":80,"output":15}}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-gpt", model, time.Now())
		if err != nil || len(got) != 1 {
			t.Fatalf("model=%q: observations=%#v err=%v", model, got, err)
		}
		if got[0].Provider != "codex" {
			t.Errorf("model=%q: got Provider=%q, want \"codex\"", model, got[0].Provider)
		}
	}
}

// TestNormalizeHermesProviderUnknownPassesThrough verifies that a provider
// string that matches no known pattern is returned verbatim.  Changing this
// to a silent drop or remapping would hide usage from unknown providers.
func TestNormalizeHermesProviderUnknownPassesThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes.jsonl")
	data := `{"type":"hermes.result","model":"llama-4-scout","provider":"custom:ollama-proxy","usage":{"input":30,"output":5}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-unknown", "llama-4-scout", time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	if got[0].Provider != "custom:ollama-proxy" {
		t.Errorf("got Provider=%q, want \"custom:ollama-proxy\"", got[0].Provider)
	}
}

// TestLoadRunArtifactHermesCacheReadPrefersLargestFieldValue verifies that
// when a Hermes result record carries both cached_input_tokens and
// cache_read_input_tokens the parser selects whichever is larger.
// Bug class: if the wrong field is taken, cache-read tokens are
// under-reported, making usage estimates systematically low and causing the
// scheduler to over-dispatch.
func TestLoadRunArtifactHermesCacheReadPrefersLargestFieldValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes-cache.jsonl")
	// cached_input_tokens (200) > cache_read_input_tokens (50): must pick 200.
	data := `{"type":"hermes.result","model":"claude-haiku-4","provider":"anthropic","usage":{"input":100,"output":10,"cache_read_input_tokens":50,"cached_input_tokens":200}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tokenlog.LoadRunArtifact(path, "hermes", "run-cache", "claude-haiku-4", time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
	if got[0].CacheReadTokens != 200 {
		t.Errorf("CacheReadTokens = %d, want 200 (largest of the two cache fields)", got[0].CacheReadTokens)
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
