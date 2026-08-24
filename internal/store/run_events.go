package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jfox/redline/internal/domain"
)

func (d *DB) RecordRunEvent(ctx context.Context, event domain.RunEvent) (domain.RunEvent, error) {
	if event.RunID == "" || event.Type == "" || event.OccurredAt.IsZero() {
		return domain.RunEvent{}, fmt.Errorf("run id, event type, and occurrence time are required")
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(event.Payload) {
		return domain.RunEvent{}, fmt.Errorf("run event payload must be valid JSON")
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO run_events (
run_id, event_type, occurred_at, payload_json
) VALUES (?, ?, ?, ?)`, event.RunID, event.Type, formatTime(event.OccurredAt), []byte(event.Payload))
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("record run event: %w", err)
	}
	event.ID, err = result.LastInsertId()
	if err != nil {
		return domain.RunEvent{}, fmt.Errorf("read run event id: %w", err)
	}
	event.OccurredAt = event.OccurredAt.UTC()
	return event, nil
}

func (d *DB) ListRunEvents(ctx context.Context, runID string, limit int) ([]domain.RunEvent, error) {
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	var exists bool
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id = ?)`, runID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("find run for events: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, run_id, event_type, occurred_at, payload_json
FROM (
    SELECT id, run_id, event_type, occurred_at, payload_json
    FROM run_events WHERE run_id = ?
    ORDER BY id DESC LIMIT ?
) ORDER BY id ASC`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("list run events: %w", err)
	}
	defer rows.Close()
	events := make([]domain.RunEvent, 0)
	for rows.Next() {
		var event domain.RunEvent
		var occurred string
		if err := rows.Scan(&event.ID, &event.RunID, &event.Type, &occurred, &event.Payload); err != nil {
			return nil, fmt.Errorf("scan run event: %w", err)
		}
		event.OccurredAt, err = parseStoredTime(occurred)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return events, nil
}
