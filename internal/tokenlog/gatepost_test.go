package tokenlog_test

import (
	"context"
	"database/sql"
	"os"
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

func TestLoadGatepostPiMapsSubscriptionProvidersAndPreservesCacheTokens(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "viewer.db")
	sessionPath := filepath.Join(directory, "pi.jsonl")
	data := `{"type":"message","id":"a1","timestamp":"2026-07-18T00:00:01Z","message":{"role":"assistant","provider":"anthropic-cli","model":"claude-opus","usage":{"input":10,"output":2,"cacheRead":30,"cacheWrite":4}}}
{"type":"message","id":"a2","timestamp":"2026-07-18T00:00:02Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5","usage":{"input":20,"output":3,"cacheRead":40,"cacheWrite":5}}}
{"type":"message","id":"a3","timestamp":"2026-07-18T00:00:03Z","message":{"role":"assistant","provider":"anthropic","model":"claude-api","usage":{"input":999,"output":999}}}
`
	if err := os.WriteFile(sessionPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", databasePath)
	_, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL, source_path TEXT, started_at INTEGER, ended_at INTEGER);
INSERT INTO sessions VALUES ('pi:s1', 'pi', ?, 1784332800000, 1784332810000);`, sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	claude, err := tokenlog.LoadGatepostPi(context.Background(), databasePath, "claude", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(claude) != 1 || claude[0].SourceID != "pi:s1:a1" || claude[0].InputTokens != 10 || claude[0].CacheReadTokens != 30 || claude[0].CacheCreationTokens != 4 {
		t.Fatalf("claude observations = %#v", claude)
	}
	codex, err := tokenlog.LoadGatepostPi(context.Background(), databasePath, "codex", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 1 || codex[0].SourceID != "pi:s1:a2" || codex[0].Model != "gpt-5" || codex[0].OutputTokens != 3 {
		t.Fatalf("codex observations = %#v", codex)
	}
}

func TestLoadGatepostPiAppliesTimestampCursor(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "viewer.db")
	sessionPath := filepath.Join(directory, "pi.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"type":"message","id":"old","timestamp":"2026-07-18T00:00:01Z","message":{"role":"assistant","provider":"anthropic-cli","usage":{"input":10}}}
{"type":"message","id":"new","timestamp":"2026-07-18T00:00:03Z","message":{"role":"assistant","provider":"anthropic-cli","usage":{"output":2}}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", databasePath)
	_, _ = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, agent TEXT NOT NULL, source_path TEXT, started_at INTEGER, ended_at INTEGER);
INSERT INTO sessions VALUES ('pi:s1', 'pi', ?, 1784332800000, 1784332810000);`, sessionPath)
	_ = db.Close()
	got, err := tokenlog.LoadGatepostPi(context.Background(), databasePath, "claude", time.Date(2026, 7, 18, 0, 0, 1, 0, time.UTC))
	if err != nil || len(got) != 1 || got[0].SourceID != "pi:s1:new" {
		t.Fatalf("observations=%#v err=%v", got, err)
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
