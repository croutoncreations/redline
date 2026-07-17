package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jfox/redline/internal/domain"
)

func TestNotificationDeliveryLifecycleAndHistory(t *testing.T) {
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

func TestRecoverPendingNotificationMarksUnknownDeliveryFailed(t *testing.T) {
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
