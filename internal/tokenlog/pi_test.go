package tokenlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Pi's JSONL schema represents the same cache-write and cache-read metrics
// under multiple aliases across versions: a flat "cacheWrite"/"cacheRead"
// pair, a flat "cacheCreation" alias for cache writes, and a nested
// "cache":{"read","write"} object. These are alternate spellings of the same
// underlying counters, not independent quantities, so a record that (as can
// happen across a schema migration) reports the same event under more than
// one alias must not have its aliases summed together.
func TestLoadPiFileDoesNotDoubleCountAliasedCacheFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	line := `{"type":"message","id":"msg1","timestamp":"2024-01-01T00:00:00Z","message":{"role":"assistant","provider":"anthropic-cli","model":"claude-3-opus","usage":{"input":10,"output":20,"cacheRead":500,"cacheWrite":1000,"cacheCreation":1000,"cache":{"read":500,"write":1000}}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	observations, err := loadPiFile(context.Background(), "session-1", path, "claude", time.Time{})
	if err != nil {
		t.Fatalf("loadPiFile: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(observations))
	}
	got := observations[0]
	if got.CacheReadTokens != 500 {
		t.Errorf("CacheReadTokens = %d, want 500 (aliases must not be summed)", got.CacheReadTokens)
	}
	if got.CacheCreationTokens != 1000 {
		t.Errorf("CacheCreationTokens = %d, want 1000 (aliases must not be summed)", got.CacheCreationTokens)
	}
}
