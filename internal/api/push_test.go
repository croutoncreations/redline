package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestPushRegistrationAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.APIToken = "owner-secret"
	s := api.NewServer(cfg, db, func() time.Time { return apiNow })
	defer s.Wait()
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	pair := call("POST", "/v1/pairing", "", cfg.APIToken)
	var p struct {
		Token string `json:"pairing_token"`
	}
	if err := json.Unmarshal(pair.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	redeemed := call("POST", "/v1/pairing/redeem", `{"pairing_token":"`+p.Token+`","name":"Phone","kind":"mobile"}`, "")
	if redeemed.Code != 204 {
		t.Fatalf("pair=%d %s", redeemed.Code, redeemed.Body)
	}
	token := redeemed.Result().Cookies()[0].Value
	valid := `{"platform":"android","provider":"example","token":"private-device-token"}`
	for _, tt := range []struct {
		method, path, body, token string
		status                    int
	}{
		{"PUT", "/v1/push/registration", valid, cfg.APIToken, 403},
		{"DELETE", "/v1/push/registration", "", cfg.APIToken, 403},
		{"GET", "/v1/push/registrations", "", token, 403},
		{"PUT", "/v1/push/registration", `null`, token, 400},
		{"PUT", "/v1/push/registration", `{"platform":"desktop","provider":"example","token":"x"}`, token, 400},
		{"PUT", "/v1/push/registration", `{"platform":"ios","provider":"BAD","token":"x"}`, token, 400},
		{"PUT", "/v1/push/registration", `{"platform":"ios","provider":"example","token":""}`, token, 400},
		{"PUT", "/v1/push/registration", `{"platform":"ios","provider":"example","token":"` + strings.Repeat("a", 4097) + `"}`, token, 400},
		{"PUT", "/v1/push/registration", valid, token, 204},
		{"PUT", "/v1/push/registration", valid, token, 204},
	} {
		w := call(tt.method, tt.path, tt.body, tt.token)
		if w.Code != tt.status {
			t.Fatalf("%s %s status=%d body=%s", tt.method, tt.path, w.Code, w.Body)
		}
	}
	listed := call("GET", "/v1/push/registrations", "", cfg.APIToken)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), "private-device-token") || !strings.Contains(listed.Body.String(), `"token_suffix":"oken"`) || !strings.Contains(listed.Body.String(), `"dropped":0`) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body)
	}
	if w := call("DELETE", "/v1/push/registration", "", token); w.Code != 204 {
		t.Fatalf("delete=%d", w.Code)
	}
	listed = call("GET", "/v1/push/registrations", "", cfg.APIToken)
	if !strings.Contains(listed.Body.String(), `"registrations":[]`) {
		t.Fatalf("list=%s", listed.Body)
	}
}

func TestNotificationPushEnabledSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig("http://unused")
			cfg.APIToken = "owner-secret"
			cfg.Notifications.Enabled = enabled
			var wakes atomic.Int64
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wakes.Add(1)
				if r.Header.Get("Authorization") != "Bearer test-shared-secret" {
					t.Error("startup auth token not sent")
				}
				var body struct {
					Hint struct {
						Kind string `json:"kind"`
					} `json:"hint"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Hint.Kind != "test" {
					t.Error("incorrect API test hint")
				}
			}))
			defer relay.Close()
			cfg.Push.RelayURL = relay.URL
			cfg.Push.AuthTokenFile = filepath.Join(t.TempDir(), "auth")
			if err := os.WriteFile(cfg.Push.AuthTokenFile, []byte("test-shared-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			s := api.NewServer(cfg, db, func() time.Time { return apiNow })
			defer s.Wait()
			pairRequest := httptest.NewRequest("POST", "http://localhost/v1/pairing", nil)
			pairRequest.Header.Set("Authorization", "Bearer "+cfg.APIToken)
			pair := httptest.NewRecorder()
			s.ServeHTTP(pair, pairRequest)
			var pairing struct {
				Token string `json:"pairing_token"`
			}
			if err := json.Unmarshal(pair.Body.Bytes(), &pairing); err != nil {
				t.Fatal(err)
			}
			redeem := httptest.NewRecorder()
			s.ServeHTTP(redeem, httptest.NewRequest("POST", "http://localhost/v1/pairing/redeem", strings.NewReader(`{"pairing_token":"`+pairing.Token+`","kind":"mobile"}`)))
			if redeem.Code != 204 {
				t.Fatalf("pair=%d", redeem.Code)
			}
			registration := httptest.NewRequest("PUT", "http://localhost/v1/push/registration", strings.NewReader(`{"platform":"ios","provider":"example","token":"opaque-token"}`))
			registration.Header.Set("Authorization", "Bearer "+redeem.Result().Cookies()[0].Value)
			registered := httptest.NewRecorder()
			s.ServeHTTP(registered, registration)
			if registered.Code != 204 {
				t.Fatalf("registration=%d", registered.Code)
			}
			r := httptest.NewRequest("POST", "http://localhost/v1/notifications/test", strings.NewReader(`{"push":true}`))
			r.Header.Set("Authorization", "Bearer "+cfg.APIToken)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			want := 409
			if enabled {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			records, err := db.ListNotificationDeliveries(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if !enabled && (len(records) != 0 || wakes.Load() != 0) {
				t.Fatal("disabled system recorded or woke a device")
			}
			if enabled {
				var delivery domain.NotificationDelivery
				if err := json.Unmarshal(w.Body.Bytes(), &delivery); err != nil {
					t.Fatal(err)
				}
				if delivery.Push == nil || !delivery.Push.Configured || delivery.Push.Woken != 1 || len(records) != 1 || wakes.Load() != 1 {
					t.Fatal("incorrect configured transport result")
				}
			}
		})
	}
}

func TestPushStartupTokenNotInConfigJSON(t *testing.T) {
	// Config contains only the file path, never the shared secret.
	cfg := testConfig("http://unused")
	cfg.Push.AuthTokenFile = filepath.Join(t.TempDir(), "auth")
	if err := os.WriteFile(cfg.Push.AuthTokenFile, []byte("shared-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "shared-secret") {
		t.Fatal("secret serialized")
	}
}
