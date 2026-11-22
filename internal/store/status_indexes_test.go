package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusIndexesRepairExistingDatabaseAndAvoidHistoryScans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"idx_runs_active_provider", "idx_runs_started", "idx_runs_completed_health", "idx_dispatch_attempts_health"} {
		if _, err := db.db.Exec(`DROP INDEX ` + name); err != nil {
			t.Fatal(err)
		}
	}
	// Colliding branch migration stamps must not suppress an index repair.
	if _, err := db.db.Exec(`INSERT INTO schema_migrations(version) VALUES(9999)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tt := range []struct {
		name, query, index string
		args               []any
	}{
		{"provider active", `SELECT COUNT(*) FROM runs WHERE provider_account_id=? AND state IN ('preparing','running')`, "idx_runs_active_provider", []any{"main"}},
		{"all active", `SELECT COUNT(*) FROM runs WHERE state IN ('preparing','running')`, "idx_runs_active_provider", nil},
		{"recent runs", `SELECT id FROM runs ORDER BY started_at DESC LIMIT 20`, "idx_runs_started", nil},
		{"run health", `SELECT SUM(CASE WHEN state='completed' THEN 1 ELSE 0 END), SUM(CASE WHEN state='failed' THEN 1 ELSE 0 END) FROM runs WHERE completed_at>=?`, "idx_runs_completed_health", []any{"2026-11-20"}},
		{"dispatch health", `SELECT COUNT(*), SUM(CASE WHEN outcome='error' AND error NOT LIKE '%context canceled%' THEN 1 ELSE 0 END) FROM dispatch_attempts WHERE completed_at>=?`, "idx_dispatch_attempts_health", []any{"2026-11-20"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := db.db.Query(`EXPLAIN QUERY PLAN `+tt.query, tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan strings.Builder
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if !strings.Contains(plan.String(), tt.index) || strings.Contains(plan.String(), "TEMP B-TREE") {
				t.Fatalf("unexpected plan: %s", plan.String())
			}
		})
	}
	var before, after int
	if err := db.db.QueryRow(`PRAGMA schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`PRAGMA schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("repeat open changed schema: %d -> %d", before, after)
	}
}
