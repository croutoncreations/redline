package tokenlog_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfox/redline/internal/tokenlog"
	_ "modernc.org/sqlite"
)

func TestLoadGatepostReadsAssistantUsageAndMapsProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL);
CREATE TABLE messages (session_id TEXT, ordinal INTEGER, role TEXT, ts INTEGER, model TEXT, context_tokens INTEGER, output_tokens INTEGER);
INSERT INTO sessions VALUES ('s1', 'claude'), ('s2', 'codex');
INSERT INTO messages VALUES
('s1', 1, 'user', 1784300000000, '', 0, 0),
('s1', 2, 'assistant', 1784300001000, 'opus', 120, 30),
('s1', 3, 'assistant', 0, 'opus', 999, 999),
('s2', 1, 'assistant', 1784300002000, 'gpt-5', 500, 40);`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	got, err := tokenlog.LoadGatepost(context.Background(), path, "claude", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "s1:2" || got[0].InputTokens != 120 || got[0].OutputTokens != 30 || got[0].Model != "opus" {
		t.Fatalf("observations = %#v", got)
	}
}

func TestLoadGatepostAppliesExclusiveTimestampCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewer.db")
	db, _ := sql.Open("sqlite", path)
	_, _ = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL);
CREATE TABLE messages (session_id TEXT, ordinal INTEGER, role TEXT, ts INTEGER, model TEXT, context_tokens INTEGER, output_tokens INTEGER);
INSERT INTO sessions VALUES ('s1', 'claude');
INSERT INTO messages VALUES ('s1', 1, 'assistant', 1784300001000, '', 10, 1), ('s1', 2, 'assistant', 1784300002000, '', 20, 2);`)
	_ = db.Close()
	cursor := time.UnixMilli(1784300001000)
	got, err := tokenlog.LoadGatepost(context.Background(), path, "claude", cursor)
	if err != nil || len(got) != 1 || got[0].SourceID != "s1:2" {
		t.Fatalf("observations=%#v err=%v", got, err)
	}
}
