package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPushEndpointsDirectAndRelay(t *testing.T) {
	for _, relayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "relay fallback"}[relayed], func(t *testing.T) {
			var method, path, body string
			handler := func(m, p, b string) (int, string, error) {
				method, path, body = m, p, b
				if m == "GET" {
					return 200, `[{"id":42,"event_type":"reset.announced","created_at":"2026-09-01T12:00:00Z","payload":{"title":"Reset","message":"Soon","data":{"kind":"SCHEDULED","key":"k","test":"true"}}}]`, nil
				}
				return 204, "", nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer phone" {
					t.Error("missing auth")
				}
				var payload map[string]string
				if r.Body != nil {
					_ = json.NewDecoder(r.Body).Decode(&payload)
				}
				encoded := ""
				if payload != nil {
					b, _ := json.Marshal(payload)
					encoded = string(b)
				}
				status, answer, _ := handler(r.Method, r.URL.RequestURI(), encoded)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(answer))
			}))
			defer server.Close()
			client := NewClient(server.URL, "phone")
			if relayed {
				server.Close() // A real direct transport failure must fall back.
				client.SetRelayFallback(RelayFallbackWithStatus(handler))
			}
			if err := client.RegisterPush("android", "fcm", "test-token"); err != nil {
				t.Fatal(err)
			}
			if method != "PUT" || path != "/v1/push/registration" {
				t.Fatalf("registration: %s %s", method, path)
			}
			var registration map[string]string
			_ = json.Unmarshal([]byte(body), &registration)
			if registration["platform"] != "android" || registration["provider"] != "fcm" || registration["token"] != "test-token" {
				t.Fatal("wrong registration body")
			}
			if err := client.UnregisterPush(); err != nil {
				t.Fatal(err)
			}
			if method != "DELETE" || path != "/v1/push/registration" {
				t.Fatalf("unregister: %s %s", method, path)
			}
			raw, err := client.FetchNotificationsAfter(20, 50)
			if err != nil {
				t.Fatal(err)
			}
			if path != "/v1/notifications?after_id=20&limit=50" {
				t.Fatal(path)
			}
			var items []struct {
				ID             int64  `json:"id"`
				EventType      string `json:"event_type"`
				Title, Message string
				Data           map[string]string `json:"data"`
				CreatedAt      string            `json:"created_at"`
			}
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].ID != 42 || items[0].Title != "Reset" || items[0].Message != "Soon" || items[0].Data["key"] != "k" || items[0].EventType != "reset.announced" || items[0].CreatedAt != "2026-09-01T12:00:00Z" {
				t.Fatalf("notifications: %+v", items)
			}
			id, err := client.LatestNotificationID()
			if err != nil || id != 42 || path != "/v1/notifications?limit=1" {
				t.Fatalf("latest: %d %v %s", id, err, path)
			}
			for _, action := range []string{"start", "stop"} {
				if err := client.FiresaleAction("k", action); err != nil {
					t.Fatal(err)
				}
				if method != "POST" || path != "/v1/reset-signals/firesale" || body != `{"action":"`+action+`","announcement_key":"k"}` {
					t.Fatalf("action: %s %s %s", method, path, body)
				}
			}
		})
	}
}

func TestPushEmptyAndErrors(t *testing.T) {
	client := NewClient("", "")
	answer := `[]`
	status := 200
	client.SetRelayFallback(RelayFallbackWithStatus(func(_, _, _ string) (int, string, error) { return status, answer, nil }))
	if id, err := client.LatestNotificationID(); err != nil || id != 0 {
		t.Fatalf("empty latest: %d %v", id, err)
	}
	if raw, err := client.FetchNotificationsAfter(0, 50); err != nil || raw != "[]" {
		t.Fatalf("empty fetch: %s %v", raw, err)
	}
	answer = `[{"id":1,"event_type":"run.failed","payload":{"title":"Job","message":"Failed"}}]`
	if raw, err := client.FetchNotificationsAfter(0, 50); err != nil || raw != `[{"id":1,"event_type":"run.failed","title":"Job","message":"Failed","data":{},"created_at":""}]` {
		t.Fatalf("missing event data: %s %v", raw, err)
	}
	answer = `[{"id":1,"payload":{"title":17}}]`
	if _, err := client.FetchNotificationsAfter(0, 50); err == nil {
		t.Fatal("malformed payload accepted")
	}
	status, answer = 401, `{"error":"pair again"}`
	for _, call := range []func() error{
		func() error { return client.RegisterPush("android", "fcm", "t") },
		client.UnregisterPush,
		func() error { _, err := client.FetchNotificationsAfter(0, 50); return err },
		func() error { _, err := client.LatestNotificationID(); return err },
		func() error { return client.FiresaleAction("k", "stop") },
	} {
		if err := call(); err == nil || !IsUnauthorized(err) {
			t.Fatalf("want auth error, got %v", err)
		}
	}
}

func TestPushHTTPRefusalDoesNotFallBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"pair again"}`))
	}))
	defer server.Close()
	client := NewClient(server.URL, "phone")
	client.SetRelayFallback(RelayFallbackWithStatus(func(_, _, _ string) (int, string, error) {
		t.Error("HTTP refusal must not fall back")
		return 200, `[]`, nil
	}))
	if _, err := client.FetchNotificationsAfter(0, 50); err == nil || !IsUnauthorized(err) {
		t.Fatalf("want direct auth failure, got %v", err)
	}
}
