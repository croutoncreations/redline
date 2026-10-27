package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

func TestNotificationDeliveryLifecycleAndHistory(t *testing.T) {
	t.Parallel()
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"version":1,"type":"run.failed"}`)
	id, err := db.CreateNotificationDelivery(context.Background(), domain.EventRunFailed, payload, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteNotificationDelivery(context.Background(), id, "failed", "hook exited 1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	deliveries, err := db.ListNotificationDeliveries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != "failed" || deliveries[0].Attempts != 1 ||
		deliveries[0].LastError != "hook exited 1" || string(deliveries[0].Payload) != string(payload) {
		t.Fatalf("deliveries = %#v", deliveries)
	}
}

func TestListNotificationDeliveriesAfterPagesByID(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 205; i++ {
		if _, err := db.CreateNotificationDelivery(t.Context(), domain.EventResetRedeemed, json.RawMessage(`{}`), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// Updating an old row must affect legacy history order, not cursor order.
	if err := db.CompleteNotificationDelivery(t.Context(), 1, "delivered", "", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	history, err := db.ListNotificationDeliveries(t.Context(), 2)
	if err != nil || len(history) != 2 || history[0].ID != 1 || history[1].ID != 205 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	for _, tt := range []struct {
		after        int64
		limit, count int
		first, last  int64
	}{
		{0, 0, 50, 1, 50}, {50, 50, 50, 51, 100}, {200, 50, 5, 201, 205},
		{0, 1000, 200, 1, 200}, {205, 50, 0, 0, 0},
	} {
		rows, err := db.ListNotificationDeliveriesAfter(t.Context(), tt.after, tt.limit)
		if err != nil || len(rows) != tt.count {
			t.Fatalf("after=%d limit=%d count=%d err=%v", tt.after, tt.limit, len(rows), err)
		}
		if len(rows) > 0 && (rows[0].ID != tt.first || rows[len(rows)-1].ID != tt.last) {
			t.Fatalf("after=%d rows=%+v", tt.after, rows)
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1].ID >= rows[i].ID {
				t.Fatal("not ascending")
			}
		}
	}
}

func TestRecoverPendingNotificationMarksUnknownDeliveryFailed(t *testing.T) {
	t.Parallel()
	db := openTaskDB(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if _, err := db.CreateNotificationDelivery(context.Background(), domain.EventRunCompleted, json.RawMessage(`{}`), now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverPendingNotificationDeliveries(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deliveries, err := db.ListNotificationDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "failed" ||
		deliveries[0].Attempts != 1 || deliveries[0].LastError != "service restarted during notification delivery" {
		t.Fatalf("deliveries=%#v err=%v", deliveries, err)
	}
}
