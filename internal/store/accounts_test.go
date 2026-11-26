package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/capacity"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

func claudeSnapshot(account string, at time.Time, weekly float64) decision.UsageSnapshot {
	return decision.UsageSnapshot{
		Provider: "claude", Account: account, ObservedAt: at, Source: "openusage",
		Weekly: decision.UsageWindow{Remaining: weekly, ResetsAt: at.Add(72 * time.Hour)},
	}
}

// Two accounts of one provider must never read each other's numbers, even
// when one was sampled more recently.
func TestTwoAccountsOfOneProviderStayIsolated(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, snapshot := range []decision.UsageSnapshot{
		claudeSnapshot("claude-personal", base, .41),
		claudeSnapshot("claude-client", base.Add(time.Minute), .58),
		// Same instant and source as the personal sample: a distinct row,
		// not a duplicate, because the account differs.
		claudeSnapshot("claude-client", base, .60),
	} {
		if err := db.SaveSnapshot(ctx, snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	personal, _, err := db.LatestSnapshot(ctx, "claude-personal", "claude")
	if err != nil || personal.Weekly.Remaining != .41 || personal.Account != "claude-personal" {
		t.Fatalf("personal = %+v, %v", personal, err)
	}
	client, _, err := db.LatestSnapshot(ctx, "claude-client", "claude")
	if err != nil || client.Weekly.Remaining != .58 {
		t.Fatalf("client = %+v, %v", client, err)
	}
	history, err := db.ListSnapshots(ctx, "claude-client", "claude", 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("client history = %d, %v", len(history), err)
	}
	if _, _, err := db.LatestSnapshot(ctx, "claude-unknown", "claude"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an account with no samples must report not found, got %v", err)
	}
}

func TestTokenObservationsAreScopedToAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	observations := []capacity.TokenObservation{
		{Provider: "claude", Account: "claude-a", Source: "redline-run", SourceID: "run-1:m", ObservedAt: at, InputTokens: 10},
		{Provider: "claude", Account: "claude-b", Source: "redline-run", SourceID: "run-2:m", ObservedAt: at, InputTokens: 20},
	}
	if _, err := db.SaveTokenObservations(ctx, observations); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListTokenObservations(ctx, "claude-b", "claude", time.Time{}, time.Time{})
	if err != nil || len(got) != 1 || got[0].InputTokens != 20 || got[0].Account != "claude-b" {
		t.Fatalf("observations = %+v, %v", got, err)
	}
}

// The supported v0.1.13 schema added provider_account_id without widening the
// table's original UNIQUE(provider, source, source_id) identity. After an
// upgrade, one account's observation must not make the same source identity
// disappear from another account.
func TestTokenObservationMigrationScopesIdentityToAccount(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the exact v0.1.13 token-observation identity while leaving the
	// rest of its schema-43 database intact.
	if _, err := raw.Exec(`
DELETE FROM schema_migrations WHERE version = 44;
DROP TABLE token_observations;
CREATE TABLE token_observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_read_tokens >= 0),
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_creation_tokens >= 0),
    confidence TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    provider_account_id TEXT NOT NULL DEFAULT '',
    UNIQUE(provider, source, source_id)
);
CREATE INDEX idx_token_observations_provider_observed
ON token_observations(provider, observed_at, id);
CREATE INDEX idx_token_observations_account_observed
ON token_observations(provider_account_id, provider, observed_at, id);
INSERT INTO token_observations (
    provider_account_id, provider, source, source_id, observed_at, input_tokens
) VALUES (
    'claude-a', 'claude', 'gatepost', 'shared-session:1',
    '2026-11-16T10:00:00.000000000Z', 10
);`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v0.1.13 database: %v", err)
	}
	observation := capacity.TokenObservation{
		Account: "claude-b", Provider: "claude", Source: "gatepost", SourceID: "shared-session:1",
		ObservedAt: time.Date(2026, 11, 16, 10, 1, 0, 0, time.UTC), InputTokens: 20,
	}
	if inserted, err := db.SaveTokenObservations(ctx, []capacity.TokenObservation{observation}); err != nil || inserted != 1 {
		t.Fatalf("save second account's observation: inserted=%d err=%v", inserted, err)
	}
	// Idempotence remains account-local after widening the identity.
	if inserted, err := db.SaveTokenObservations(ctx, []capacity.TokenObservation{observation}); err != nil || inserted != 0 {
		t.Fatalf("repeat second account's observation: inserted=%d err=%v", inserted, err)
	}
	for account, wantTokens := range map[string]int64{"claude-a": 10, "claude-b": 20} {
		got, err := db.ListTokenObservations(ctx, account, "claude", time.Time{}, time.Time{})
		if err != nil || len(got) != 1 || got[0].InputTokens != wantTokens {
			t.Fatalf("%s observations = %+v, %v", account, got, err)
		}
	}
	db.Close()

	// Reopening the upgraded fixture preserves both records and the repair.
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.ListTokenObservations(ctx, "claude-b", "claude", time.Time{}, time.Time{})
	if err != nil || len(got) != 1 || got[0].InputTokens != 20 {
		t.Fatalf("reopened observations = %+v, %v", got, err)
	}
}

// Upgrading a single-account install keeps its history: rows recorded before
// accounts existed are adopted by the provider's only account. With several
// accounts they stay unowned, since nothing says whose they were.
func TestLegacyUsageIsAdoptedOnlyByASoleAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, snapshot := range []decision.UsageSnapshot{claudeSnapshot("", base, .5), {
		Provider: "codex", ObservedAt: base, Source: "openusage",
		Weekly: decision.UsageWindow{Remaining: .3, ResetsAt: base.Add(time.Hour)},
	}} {
		if err := db.SaveSnapshot(ctx, snapshot, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SaveTokenObservations(ctx, []capacity.TokenObservation{
		{Provider: "codex", Source: "gatepost", SourceID: "x", ObservedAt: base, InputTokens: 1},
	}); err != nil {
		t.Fatal(err)
	}
	adopted, err := db.AdoptLegacyUsage(ctx, map[string][]string{
		"claude": {"claude-a", "claude-b"},
		"codex":  {"codex-main"},
	})
	if err != nil || adopted != 2 {
		t.Fatalf("adopted = %d, %v; want the codex snapshot and observation", adopted, err)
	}
	if _, _, err := db.LatestSnapshot(ctx, "codex-main", "codex"); err != nil {
		t.Fatalf("sole codex account should own its legacy snapshot: %v", err)
	}
	for _, account := range []string{"claude-a", "claude-b"} {
		if _, _, err := db.LatestSnapshot(ctx, account, "claude"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s must not inherit an unattributable legacy row: %v", account, err)
		}
	}
	if got, _ := db.ListTokenObservations(ctx, "codex-main", "codex", time.Time{}, time.Time{}); len(got) != 1 {
		t.Fatalf("codex observations = %d", len(got))
	}
	// Idempotent across restarts.
	if again, err := db.AdoptLegacyUsage(ctx, map[string][]string{"codex": {"codex-main"}}); err != nil || again != 0 {
		t.Fatalf("second adoption = %d, %v", again, err)
	}
	db.Close()
	// Restart: the migration is recorded and data persists.
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, _, err := reopened.LatestSnapshot(ctx, "codex-main", "codex"); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

// A database written by the previous schema (no account column) migrates in
// place and keeps every row.
func TestAccountMigrationFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, claudeSnapshot("", base, .5), nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// Roll the database back to the v29 shape: drop the columns' index and
	// forget the migration, as an installed older build would have it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP INDEX idx_usage_snapshots_identity;
DROP INDEX IF EXISTS idx_usage_snapshots_account_observed;
ALTER TABLE usage_snapshots DROP COLUMN provider_account_id;
ALTER TABLE usage_snapshots DROP COLUMN banked_resets_expiries;
ALTER TABLE usage_snapshots DROP COLUMN plan;
ALTER TABLE usage_snapshots DROP COLUMN source_stale;
DROP TABLE token_observations;
CREATE TABLE token_observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_read_tokens >= 0),
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cache_creation_tokens >= 0),
    confidence TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, source, source_id)
);
CREATE INDEX idx_token_observations_provider_observed
ON token_observations(provider, observed_at, id);
CREATE UNIQUE INDEX idx_usage_snapshots_identity ON usage_snapshots(provider, observed_at, source);
DELETE FROM schema_migrations WHERE version >= 30;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	migrated, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if _, err := migrated.AdoptLegacyUsage(ctx, map[string][]string{"claude": {"claude-main"}}); err != nil {
		t.Fatal(err)
	}
	got, _, err := migrated.LatestSnapshot(ctx, "claude-main", "claude")
	if err != nil || got.Weekly.Remaining != .5 {
		t.Fatalf("migrated snapshot = %+v, %v", got, err)
	}
}
