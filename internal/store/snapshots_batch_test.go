package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"modernc.org/sqlite"
)

func seedAllowanceHistory(t testing.TB, count int) (*DB, string, []int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for i := range count {
		s := decision.UsageSnapshot{Account: "main", Provider: "claude", Source: "native", Confidence: "high", ObservedAt: at.Add(time.Duration(i) * time.Second),
			Weekly: decision.UsageWindow{Remaining: .8, ResetsAt: at.Add(7 * 24 * time.Hour)},
			Allowances: []decision.AllowanceWindow{
				{Key: "weekly", SourceLabel: "Weekly", Scope: "account", Role: "weekly", Remaining: .8, ResetsAt: at.Add(7 * 24 * time.Hour), PeriodDurationSeconds: 604800},
				{Key: "a-short", SourceLabel: "Session", Scope: "account", Role: "short", Remaining: .5, ResetsAt: at.Add(5 * time.Hour), PeriodDurationSeconds: 18000, ResetInferred: true},
			}}
		if err := db.SaveSnapshot(context.Background(), s, nil); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.db.Query(`SELECT id FROM usage_snapshots ORDER BY observed_at`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	return db, path, ids
}

type queryCountConnector struct {
	path    string
	queries *atomic.Int64
}

func (c queryCountConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c queryCountConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.path)
	if err != nil {
		return nil, err
	}
	return queryCountConn{Conn: conn, queries: c.queries}, nil
}

type queryCountConn struct {
	driver.Conn
	queries *atomic.Int64
}

func (c queryCountConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func TestListSnapshotsBatchesAllowancesAndPreservesOrder(t *testing.T) {
	db, path, ids := seedAllowanceHistory(t, 501)
	// Older schemas can have observations with no allowance rows.
	if _, err := db.db.Exec(`DELETE FROM usage_allowance_windows WHERE snapshot_id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	// A different account must not leak into the selected history.
	s := decision.UsageSnapshot{Account: "other", Provider: "claude", Source: "native", Confidence: "high", ObservedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), Weekly: decision.UsageWindow{Remaining: .1, ResetsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}}
	if err := db.SaveSnapshot(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	db.db.Close()
	var queries atomic.Int64
	db.db = sql.OpenDB(queryCountConnector{path: path, queries: &queries})
	db.db.SetMaxOpenConns(1)
	for _, limit := range []int{0, 500, 501, 5000} {
		queries.Store(0)
		got, err := db.ListSnapshots(t.Context(), "main", "claude", limit)
		if err != nil {
			t.Fatal(err)
		}
		want := min(limit, 501)
		if limit <= 0 {
			want = 500
		}
		if len(got) != want {
			t.Fatalf("limit %d: got %d snapshots, want %d", limit, len(got), want)
		}
		if n := queries.Load(); n != int64(1+(want+499)/500) {
			t.Fatalf("limit %d: %d queries for %d snapshots", limit, n, want)
		}
		for i, snapshot := range got {
			if snapshot.Account != "main" {
				t.Fatalf("wrong account: %s", snapshot.Account)
			}
			if i > 0 && !got[i-1].ObservedAt.Before(snapshot.ObservedAt) {
				t.Fatal("history not chronological")
			}
			if want == 501 && i == 0 {
				if snapshot.Allowances == nil || len(snapshot.Allowances) != 0 {
					t.Fatal("legacy observation should have an empty allowance list")
				}
				continue
			}
			if len(snapshot.Allowances) != 2 || snapshot.Allowances[0].Key != "a-short" || !snapshot.Allowances[0].ResetInferred || snapshot.Allowances[1].Key != "weekly" {
				t.Fatalf("allowance association/order lost: %+v", snapshot.Allowances)
			}
		}
	}
	queries.Store(0)
	got, err := db.ListSnapshots(t.Context(), "absent", "claude", 500)
	if err != nil || len(got) != 0 || queries.Load() != 1 {
		t.Fatalf("empty history: %d snapshots, %d queries, %v", len(got), queries.Load(), err)
	}
}

func TestSnapshotRevisionChangesOnlyAfterCommittedHistoryChanges(t *testing.T) {
	db, _, _ := seedAllowanceHistory(t, 1)
	before := db.SnapshotRevision()
	snapshots, err := db.ListSnapshots(t.Context(), "main", "claude", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshots[0]
	if err := db.SaveSnapshot(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if db.SnapshotRevision() != before {
		t.Fatal("unchanged duplicate invalidated cache")
	}
	s.SourceStale = true
	if err := db.SaveSnapshot(t.Context(), s, nil); err != nil {
		t.Fatal(err)
	}
	if db.SnapshotRevision() != before+1 {
		t.Fatal("staleness update did not invalidate cache")
	}
	s.Weekly.Remaining = -1
	if err := db.SaveSnapshot(t.Context(), s, nil); err == nil {
		t.Fatal("invalid write succeeded")
	}
	if db.SnapshotRevision() != before+1 {
		t.Fatal("failed save invalidated cache")
	}
}

func BenchmarkSnapshotAllowanceLoading(b *testing.B) {
	db, _, ids := seedAllowanceHistory(b, 500)
	for _, batch := range []bool{false, true} {
		b.Run(fmt.Sprintf("batch=%t", batch), func(b *testing.B) {
			for b.Loop() {
				if batch {
					if _, err := db.loadSnapshotAllowances(context.Background(), ids); err != nil {
						b.Fatal(err)
					}
				} else {
					for _, id := range ids {
						if _, err := db.loadAllowances(context.Background(), id); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
		})
	}
}
