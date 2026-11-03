package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

// ObserveResetAnnouncement retains each kind separately. Only a kind above
// every claimed kind can alert; a tracker downgrade cannot.
func (d *DB) ObserveResetAnnouncement(ctx context.Context, a resetsignal.Announcement, now time.Time) (bool, error) {
	payload, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	if _, err := d.db.ExecContext(ctx, `INSERT INTO reset_announcements(announcement_key,kind,first_seen_at,last_seen_at,payload_json) VALUES(?,?,?,?,?)
ON CONFLICT(announcement_key,kind) DO UPDATE SET last_seen_at=excluded.last_seen_at,payload_json=excluded.payload_json`, a.Key, a.Kind, formatTime(now), formatTime(now), payload); err != nil {
		return false, fmt.Errorf("observe reset announcement: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT kind,retracted_at IS NOT NULL FROM reset_announcements WHERE announcement_key=? AND notified_at IS NOT NULL`, a.Key)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	needed := true
	for rows.Next() {
		var kind resetsignal.Kind
		var retracted bool
		if err := rows.Scan(&kind, &retracted); err != nil {
			return false, err
		}
		if retracted || announcementRank(kind) >= announcementRank(a.Kind) {
			needed = false
		}
	}
	return needed, rows.Err()
}

// announcementKinds lists every kind announcementRank ranks above zero.
var announcementKinds = []resetsignal.Kind{resetsignal.KindPromised, resetsignal.KindScheduled, resetsignal.KindCompleted}

// rankSQL renders announcementRank as a SQL CASE on column, so the claim SQL
// and the Go precheck share one escalation ladder.
func rankSQL(column string) string {
	expr := "CASE " + column
	for _, kind := range announcementKinds {
		expr += fmt.Sprintf(" WHEN '%s' THEN %d", kind, announcementRank(kind))
	}
	return expr + " ELSE 0 END"
}

func announcementRank(kind resetsignal.Kind) int {
	switch kind {
	case resetsignal.KindPromised:
		return 1
	case resetsignal.KindScheduled:
		return 2
	case resetsignal.KindCompleted:
		return 3
	}
	return 0
}

// ClaimResetAnnouncement atomically checks escalation and claims before delivery.
// Failed hooks are not retried, matching reset.expiring's at-most-once policy.
func (d *DB) ClaimResetAnnouncement(ctx context.Context, a resetsignal.Announcement, now time.Time) (bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE reset_announcements SET notified_at=?
WHERE announcement_key=? AND kind=? AND notified_at IS NULL
AND NOT EXISTS (SELECT 1 FROM reset_announcements AS previous WHERE previous.announcement_key=? AND
 (previous.retracted_at IS NOT NULL OR (previous.notified_at IS NOT NULL AND
 `+rankSQL("previous.kind")+` >= ?)))`, formatTime(now), a.Key, a.Kind, a.Key, announcementRank(a.Kind))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (d *DB) UnretractedResetAnnouncements(ctx context.Context) ([]resetsignal.Announcement, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT payload_json FROM reset_announcements WHERE notified_at IS NOT NULL AND retracted_at IS NULL ORDER BY announcement_key, `+rankSQL("kind")+` DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []resetsignal.Announcement
	seen := map[string]bool{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var a resetsignal.Announcement
		if err := json.Unmarshal(payload, &a); err != nil {
			return nil, err
		}
		if !seen[a.Key] {
			result = append(result, a)
			seen[a.Key] = true
		}
	}
	return result, rows.Err()
}

func (d *DB) ClaimResetAnnouncementRetraction(ctx context.Context, key string, now time.Time) (bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE reset_announcements SET retracted_at=? WHERE announcement_key=?
AND NOT EXISTS (SELECT 1 FROM reset_announcements AS previous WHERE previous.announcement_key=? AND previous.retracted_at IS NOT NULL)
AND EXISTS (SELECT 1 FROM reset_announcements AS previous WHERE previous.announcement_key=? AND previous.notified_at IS NOT NULL)`, formatTime(now), key, key, key)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count > 0, nil
}
