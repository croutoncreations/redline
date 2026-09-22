package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (d *DB) SetProviderPaused(ctx context.Context, provider string, paused bool) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO provider_controls(provider_account_id, paused, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(provider_account_id) DO UPDATE SET paused = excluded.paused, updated_at = CURRENT_TIMESTAMP`,
		provider, paused)
	if err != nil {
		return fmt.Errorf("set provider pause state: %w", err)
	}
	return nil
}

func (d *DB) ProviderPaused(ctx context.Context, provider string) (bool, error) {
	var paused bool
	err := d.db.QueryRowContext(ctx, `SELECT paused FROM provider_controls WHERE provider_account_id = ?`, provider).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read provider pause state: %w", err)
	}
	return paused, nil
}

func (d *DB) SetProviderPolicy(ctx context.Context, provider, policy string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO provider_controls(provider_account_id, paused, policy_name, updated_at)
VALUES (?, 0, ?, CURRENT_TIMESTAMP)
ON CONFLICT(provider_account_id) DO UPDATE SET policy_name = excluded.policy_name, updated_at = CURRENT_TIMESTAMP`,
		provider, policy)
	if err != nil {
		return fmt.Errorf("set provider policy: %w", err)
	}
	return nil
}

func (d *DB) ProviderPolicy(ctx context.Context, provider string) (string, error) {
	var policy string
	err := d.db.QueryRowContext(ctx, `SELECT policy_name FROM provider_controls WHERE provider_account_id = ?`, provider).Scan(&policy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read provider policy: %w", err)
	}
	return policy, nil
}

func (d *DB) SetProviderMaxConcurrentRuns(ctx context.Context, provider string, limit int) error {
	if limit < 0 {
		return fmt.Errorf("provider max concurrent runs cannot be negative")
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO provider_controls(
provider_account_id, paused, max_concurrent_runs, updated_at
) VALUES (?, 0, ?, CURRENT_TIMESTAMP)
ON CONFLICT(provider_account_id) DO UPDATE SET
max_concurrent_runs = excluded.max_concurrent_runs, updated_at = CURRENT_TIMESTAMP`,
		provider, limit)
	if err != nil {
		return fmt.Errorf("set provider max concurrent runs: %w", err)
	}
	return nil
}

func (d *DB) ProviderMaxConcurrentRuns(ctx context.Context, provider string) (int, error) {
	var limit int
	err := d.db.QueryRowContext(ctx, `SELECT max_concurrent_runs FROM provider_controls
WHERE provider_account_id = ?`, provider).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read provider max concurrent runs: %w", err)
	}
	return limit, nil
}
