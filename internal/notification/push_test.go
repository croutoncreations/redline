package notification_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/push"
	"github.com/croutoncreations/redline/internal/store"
)

func TestRecordedDeliveryWakesRegisteredDevicesWithoutContent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Now().UTC()
	for _, id := range []string{"one", "two", "revoked", "rotated"} {
		issuer := "current"
		if id == "rotated" {
			issuer = "old"
		}
		if err := db.CreateDeviceSession(t.Context(), store.DeviceSession{ID: id, Name: id, Kind: "mobile", CreatedAt: at, LastUsedAt: at}, id, issuer); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertPushRegistration(t.Context(), store.PushRegistration{DeviceSessionID: id, Platform: "ios", Provider: "example", Token: id}, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RevokeDeviceSession(t.Context(), "revoked", at); err != nil {
		t.Fatal(err)
	}
	received := make(chan push.Wake, 8)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		for _, secret := range []string{"title", "message", "secret title", "secret message"} {
			if strings.Contains(string(body), secret) {
				t.Error("notification content leaked")
			}
		}
		var wake push.Wake
		if err := json.Unmarshal(body, &wake); err != nil {
			t.Error(err)
		}
		records, err := db.ListNotificationDeliveries(t.Context(), 10)
		if err != nil || len(records) == 0 || records[0].ID != wake.Hint.DeliveryID {
			t.Error("wake preceded recording")
		}
		received <- wake
	}))
	defer relay.Close()
	dispatcher := push.NewDispatcher(db, "current", &push.RelayTransport{BaseURL: relay.URL, AuthToken: "test-secret"}, nil)
	defer dispatcher.Close()
	service := notification.Service{Enabled: true, Events: map[string]bool{domain.EventResetExpiring: true}, Store: db, Sink: notification.CommandSink{}, Push: dispatcher}
	event := domain.NotificationEvent{Type: domain.EventResetExpiring, Title: "secret title", Message: "secret message"}
	if err := service.Notify(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case wake := <-received:
			if wake.Hint.DeliveryID != 1 || wake.Hint.Kind != "reset" || seen[wake.Token] {
				t.Fatalf("bad or duplicate wake hint=%+v", wake.Hint)
			}
			seen[wake.Token] = true
		case <-time.After(2 * time.Second):
			t.Fatal("missing wake")
		}
	}
	if !seen["one"] || !seen["two"] {
		t.Fatal("revoked or rotated registration used")
	}
	select {
	case <-received:
		t.Fatal("extra wake")
	default:
	}
	// A non-subscribed event does not record or wake.
	event.Type = domain.EventRunStarted
	if err := service.Notify(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	records, err := db.ListNotificationDeliveries(t.Context(), 10)
	if err != nil || len(records) != 1 {
		t.Fatal("subscription ignored")
	}
	// The test uses the same record/hook path, but its push hint is always test.
	delivery, err := service.TestPush(t.Context(), event)
	if err != nil || delivery.Push == nil || delivery.Push.Woken != 2 {
		t.Fatalf("test result=%+v err=%v", delivery.Push, err)
	}
	for range 2 {
		wake := <-received
		if wake.Hint.Kind != "test" || wake.Hint.DeliveryID != delivery.ID {
			t.Fatal("wrong test hint")
		}
	}
}
