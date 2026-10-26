package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestListNotificationsAfterID(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 205; i++ {
		if _, err := db.CreateNotificationDelivery(t.Context(), domain.EventResetRedeemed, json.RawMessage(`{}`), apiNow.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompleteNotificationDelivery(t.Context(), 1, "delivered", "", apiNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := api.NewServer(testConfig("http://unused.invalid"), db, func() time.Time { return apiNow })
	for _, tt := range []struct {
		query         string
		status, count int
		first, last   int64
	}{
		{"", 200, 50, 1, 157},
		{"?after_id=0", 200, 50, 1, 50},
		{"?after_id=50&limit=50", 200, 50, 51, 100},
		{"?after_id=200&limit=50", 200, 5, 201, 205},
		{"?after_id=0&limit=999", 200, 200, 1, 200},
		{"?after_id=205", 200, 0, 0, 0},
		{"?after_id=9223372036854775807", 200, 0, 0, 0},
		{"?after_id=bad", 400, 0, 0, 0},
		{"?after_id=", 400, 0, 0, 0},
		{"?after_id=9223372036854775808", 400, 0, 0, 0},
	} {
		t.Run(tt.query, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/notifications"+tt.query, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tt.status != 200 {
				return
			}
			var rows []domain.NotificationDelivery
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != tt.count {
				t.Fatalf("count=%d want=%d", len(rows), tt.count)
			}
			if len(rows) > 0 && (rows[0].ID != tt.first || rows[len(rows)-1].ID != tt.last) {
				t.Fatalf("rows=%+v", rows)
			}
		})
	}
}

func TestNotificationTestEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name, body, token string
		enabled           bool
		status            int
		provider          string
	}{
		{"default", `{}`, "secret", true, 200, "codex-main"},
		{"explicit", `{"event":"firesale.started","provider":"claude-main"}`, "secret", true, 200, "claude-main"},
		{"disabled", `{}`, "secret", false, 409, ""},
		{"unauthorized", `{}`, "", true, 401, ""},
		{"cookie forbidden", `{}`, "cookie", true, 403, ""},
		{"wrong token", `{}`, "wrong", true, 401, ""},
		{"unknown event", `{"event":"oops"}`, "secret", true, 400, ""},
		{"unknown provider", `{"provider":"oops"}`, "secret", true, 400, ""},
		{"null", `null`, "secret", true, 400, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig("http://unused.invalid")
			cfg.APIToken = "secret"
			cfg.Notifications = config.Notifications{Enabled: tt.enabled, Events: []string{"run.failed"}}
			h := api.NewServer(cfg, db, func() time.Time { return apiNow })
			r := httptest.NewRequest(http.MethodPost, "/v1/notifications/test", strings.NewReader(tt.body))
			r.Host = "127.0.0.1"
			if tt.token == "cookie" {
				r.AddCookie(&http.Cookie{Name: "redline_api_session", Value: "secret"})
			} else if tt.token != "" {
				r.Header.Set("Authorization", "Bearer "+tt.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			rows, err := db.ListNotificationDeliveries(t.Context(), 50)
			if err != nil {
				t.Fatal(err)
			}
			if tt.status != 200 {
				if len(rows) != 0 {
					t.Fatal(rows)
				}
				if tt.status == 409 && !strings.Contains(w.Body.String(), "notifications.enabled: true") {
					t.Fatal(w.Body.String())
				}
				return
			}
			var delivery domain.NotificationDelivery
			if err := json.Unmarshal(w.Body.Bytes(), &delivery); err != nil {
				t.Fatal(err)
			}
			var event domain.NotificationEvent
			if err := json.Unmarshal(delivery.Payload, &event); err != nil {
				t.Fatal(err)
			}
			if delivery.ID == 0 || delivery.Status != "delivered" || len(rows) != 1 || event.ProviderAccountID != tt.provider || !strings.HasPrefix(event.Title, "Test: ") || event.Data["test"] != "true" {
				t.Fatalf("delivery=%+v event=%+v rows=%v", delivery, event, rows)
			}
		})
	}
}

func TestNotificationTestFallbackProviderAndFailedDelivery(t *testing.T) {
	for _, command := range []string{"", "exit 7"} {
		t.Run(command, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig("http://unused.invalid")
			delete(cfg.Providers, "codex-main")
			cfg.APIToken = "secret"
			cfg.Notifications = config.Notifications{Enabled: true, Command: command}
			h := api.NewServer(cfg, db, func() time.Time { return apiNow })
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/notifications/test", strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var record domain.NotificationDelivery
			if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil || w.Code != 200 {
				t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body, err)
			}
			var event domain.NotificationEvent
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.ProviderAccountID != "claude-main" {
				t.Fatal(event)
			}
			if command == "" && record.Status != "delivered" {
				t.Fatal(record)
			}
			if command != "" && (record.Status != "failed" || !strings.Contains(record.LastError, "7")) {
				t.Fatal(record)
			}
			rows, err := db.ListNotificationDeliveries(t.Context(), 50)
			if err != nil || len(rows) != 1 || rows[0].Status != record.Status || rows[0].ID != record.ID {
				t.Fatalf("rows=%+v record=%+v err=%v", rows, record, err)
			}
		})
	}
}
