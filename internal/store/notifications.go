package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func (d *DB) CreateNotificationDelivery(
	ctx context.Context,
	eventType string,
	payload json.RawMessage,
	now time.Time,
) (int64, error) {
	if eventType == "" || len(payload) == 0 || now.IsZero() {
		return 0, fmt.Errorf("notification event type, payload, and timestamp are required")
	}
	result, err := d.db.ExecContext(ctx, `INSERT INTO notification_deliveries (
event_type, status, payload_json, attempts, created_at, updated_at
) VALUES (?, 'pending', ?, 0, ?, ?)`, eventType, []byte(payload), formatTime(now), formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("create notification delivery: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read notification delivery id: %w", err)
	}
	return id, nil
}

func (d *DB) CompleteNotificationDelivery(
	ctx context.Context,
	id int64,
	status, lastError string,
	now time.Time,
) error {
	if status != "delivered" && status != "failed" {
		return fmt.Errorf("notification status must be delivered or failed")
	}
	result, err := d.db.ExecContext(ctx, `UPDATE notification_deliveries SET
status = ?, attempts = attempts + 1, last_error = ?, updated_at = ?
WHERE id = ? AND status = 'pending'`, status, lastError, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("complete notification delivery: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return fmt.Errorf("%w: pending notification delivery %d", ErrNotFound, id)
	}
	return nil
}

func (d *DB) ListNotificationDeliveries(ctx context.Context, limit int) ([]domain.NotificationDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id, event_type, status, payload_json,
attempts, last_error, created_at, updated_at FROM notification_deliveries
ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]domain.NotificationDelivery, 0)
	for rows.Next() {
		var delivery domain.NotificationDelivery
		var payload []byte
		var createdAt, updatedAt string
		if err := rows.Scan(&delivery.ID, &delivery.EventType, &delivery.Status, &payload,
			&delivery.Attempts, &delivery.LastError, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan notification delivery: %w", err)
		}
		delivery.Payload = json.RawMessage(payload)
		if delivery.CreatedAt, err = parseStoredTime(createdAt); err != nil {
			return nil, err
		}
		if delivery.UpdatedAt, err = parseStoredTime(updatedAt); err != nil {
			return nil, err
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (d *DB) RecoverPendingNotificationDeliveries(ctx context.Context, now time.Time) error {
	_, err := d.db.ExecContext(ctx, `UPDATE notification_deliveries SET
status = 'failed', attempts = attempts + 1,
last_error = 'service restarted during notification delivery', updated_at = ?
WHERE status = 'pending'`, formatTime(now))
	if err != nil {
		return fmt.Errorf("recover pending notification deliveries: %w", err)
	}
	return nil
}
