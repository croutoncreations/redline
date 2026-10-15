package store

import (
	"context"
	"time"
)

// ForceBranchLeaseForTest writes a lease row directly, to model a lease left
// behind by a run that ended without releasing it.
func (d *DB) ForceBranchLeaseForTest(ctx context.Context, repository, branch, runID string, now time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT OR REPLACE INTO branch_leases(repository, branch, run_id, acquired_at)
VALUES (?, ?, ?, ?)`, canonicalRepositoryPath(repository), branch, runID, formatTime(now))
	return err
}
