package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jfox/redline/internal/capacity"
)

func (d *DB) SaveTokenObservations(ctx context.Context, observations []capacity.TokenObservation) (int, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin token observation save: %w", err)
	}
	defer tx.Rollback()
	inserted := 0
	for _, observation := range observations {
		if strings.TrimSpace(observation.Provider) == "" || strings.TrimSpace(observation.Source) == "" || strings.TrimSpace(observation.SourceID) == "" || observation.ObservedAt.IsZero() {
			return 0, fmt.Errorf("token observation requires provider, source, source_id, and observed_at")
		}
		if observation.InputTokens < 0 || observation.OutputTokens < 0 || observation.CacheReadTokens < 0 || observation.CacheCreationTokens < 0 {
			return 0, fmt.Errorf("token observation counts cannot be negative")
		}
		result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO token_observations (
provider, source, source_id, observed_at, model, input_tokens, output_tokens,
cache_read_tokens, cache_creation_tokens, confidence
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, observation.Provider, observation.Source, observation.SourceID,
			formatTokenTime(observation.ObservedAt), observation.Model, observation.InputTokens,
			observation.OutputTokens, observation.CacheReadTokens, observation.CacheCreationTokens, observation.Confidence)
		if err != nil {
			return 0, fmt.Errorf("save token observation: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count saved token observations: %w", err)
		}
		inserted += int(count)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit token observations: %w", err)
	}
	return inserted, nil
}

func (d *DB) ListTokenObservations(ctx context.Context, provider string, after, through time.Time) ([]capacity.TokenObservation, error) {
	query := `SELECT provider, source, source_id, observed_at, model, input_tokens, output_tokens,
cache_read_tokens, cache_creation_tokens, confidence FROM token_observations WHERE provider = ?`
	args := []any{provider}
	if !after.IsZero() {
		query += ` AND observed_at > ?`
		args = append(args, formatTokenTime(after))
	}
	if !through.IsZero() {
		query += ` AND observed_at <= ?`
		args = append(args, formatTokenTime(through))
	}
	query += ` ORDER BY observed_at, id`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list token observations: %w", err)
	}
	defer rows.Close()
	var result []capacity.TokenObservation
	for rows.Next() {
		var observation capacity.TokenObservation
		var observedAt string
		if err := rows.Scan(&observation.Provider, &observation.Source, &observation.SourceID, &observedAt,
			&observation.Model, &observation.InputTokens, &observation.OutputTokens, &observation.CacheReadTokens,
			&observation.CacheCreationTokens, &observation.Confidence); err != nil {
			return nil, fmt.Errorf("scan token observation: %w", err)
		}
		observation.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("parse token observation time: %w", err)
		}
		result = append(result, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token observations: %w", err)
	}
	return result, nil
}

func formatTokenTime(value time.Time) string {
	// Fixed-width fractions preserve chronological order in SQLite TEXT keys.
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func (d *DB) LatestTokenObservationTime(ctx context.Context, provider, source string) (time.Time, error) {
	var value string
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(observed_at), '') FROM token_observations WHERE provider = ? AND source = ?`, provider, source).Scan(&value)
	if err != nil {
		return time.Time{}, fmt.Errorf("read latest token observation time: %w", err)
	}
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse latest token observation time: %w", err)
	}
	return parsed, nil
}
