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
