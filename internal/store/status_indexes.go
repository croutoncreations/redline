package store

import (
	"context"
	"database/sql"
	"fmt"
)

// These index-only repairs are idempotent even for databases whose migration
// stamps came from another branch. They run on the next normal store open.
func ensureStatusIndexes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_runs_active_provider
ON runs(provider_account_id, state) WHERE state IN ('preparing', 'running');
CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_completed_health ON runs(completed_at, state);
CREATE INDEX IF NOT EXISTS idx_dispatch_attempts_health
ON dispatch_attempts(completed_at, outcome, error);
`)
	if err != nil {
		return fmt.Errorf("index status queries: %w", err)
	}
	return nil
}
