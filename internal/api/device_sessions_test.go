package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func TestPairedDeviceCreatesTaskAndStartsRun(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := testConfig("http://unused")
	cfg.APIToken = "owner-secret"
	snapshot := decision.UsageSnapshot{Provider: "codex", ObservedAt: apiNow, Weekly: decision.UsageWindow{Remaining: .6, ResetsAt: apiNow.Add(48 * time.Hour)}, Source: "test", Confidence: "high"}
	s := api.NewDemoServer(cfg, db, func() time.Time { return apiNow }, map[string]decision.UsageSnapshot{"codex-main": snapshot}, &fakeHarnessDiscoverer{}, fakeExecutor{execute: func(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error { return nil }})
	defer s.Wait()
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	pair := call("POST", "/v1/pairing", "", cfg.APIToken)
	var pairing struct {
		Token string `json:"pairing_token"`
	}
	if err := json.Unmarshal(pair.Body.Bytes(), &pairing); err != nil {
		t.Fatal(err)
	}
	redeem := call("POST", "/v1/pairing/redeem", `{"pairing_token":"`+pairing.Token+`","kind":"mobile","name":"Phone"}`, "")
	if redeem.Code != 204 {
		t.Fatalf("redeem=%d %s", redeem.Code, redeem.Body)
	}
	token := redeem.Result().Cookies()[0].Value
	profile := call("POST", "/v1/profiles", `{"id":"profile","provider_account_id":"codex-main","harness_type":"codex-cli","workspace_provider":"devx"}`, token)
	if profile.Code != 201 {
		t.Fatalf("profile=%d %s", profile.Code, profile.Body)
	}
	task := call("POST", "/v1/tasks", `{"id":"job","name":"Job","execution_profile_id":"profile","type":"one_off","prompt":"Do work"}`, token)
	if task.Code != 201 {
		t.Fatalf("task=%d %s", task.Code, task.Body)
	}
	if w := call("GET", "/v1/tasks", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "Job") {
		t.Fatalf("list=%d %s", w.Code, w.Body)
	}
	if w := call("PATCH", "/v1/tasks/job", `{"name":"Updated"}`, token); w.Code != 200 {
		t.Fatalf("edit=%d %s", w.Code, w.Body)
	}
	for _, action := range []string{"pause", "resume"} {
		if w := call("POST", "/v1/providers/codex-main/"+action, "", token); w.Code != 200 {
			t.Fatalf("%s=%d %s", action, w.Code, w.Body)
		}
	}
	run := call("POST", "/v1/tasks/job/dispatch", "", token)
	if run.Code != 202 || !strings.Contains(run.Body.String(), `"run":`) {
		t.Fatalf("run=%d %s", run.Code, run.Body)
	}
}

func TestDeviceSessionPermissionsAndRevocation(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "redemption enabled"}[allow], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "redline.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := testConfig("http://unused")
			cfg.APIToken = "owner-secret"
			cfg.API.PairedDevices.AllowResetRedemption = allow
			s := api.NewServer(cfg, db, func() time.Time { return apiNow })
			request := func(method, path, body, token string, cookie bool) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
				if cookie {
					r.AddCookie(&http.Cookie{Name: "redline_api_session", Value: token})
				} else if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				return w
			}
			pair := request("POST", "/v1/pairing", "", cfg.APIToken, false)
			var pairing struct {
				Token string `json:"pairing_token"`
			}
			if err := json.Unmarshal(pair.Body.Bytes(), &pairing); err != nil {
				t.Fatal(err)
			}
			redeem := request("POST", "/v1/pairing/redeem", `{"pairing_token":"`+pairing.Token+`"}`, "", false)
			cookies := redeem.Result().Cookies()
			if redeem.Code != 204 || len(cookies) != 1 || cookies[0].Value == cfg.APIToken {
				t.Fatalf("redeem=%d cookies=%v", redeem.Code, cookies)
			}
			token := cookies[0].Value
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var hash string
			if err := raw.QueryRow(`SELECT token_hash FROM device_sessions`).Scan(&hash); err != nil || hash != fmt.Sprintf("%x", sha256.Sum256([]byte(token))) {
				t.Fatalf("stored hash=%q err=%v", hash, err)
			}
			// Reconstructing the server must not revoke persisted pairings.
			s = api.NewServer(cfg, db, func() time.Time { return apiNow })
			for _, cookie := range []bool{false, true} {
				if w := request("GET", "/v1/tasks", "", token, cookie); w.Code != 200 {
					t.Fatalf("tasks=%d %s", w.Code, w.Body)
				}
				for _, path := range []string{"/v1/notifications/test", "/v1/pairing"} {
					if w := request("POST", path, `{}`, token, cookie); w.Code != 403 {
						t.Fatalf("%s=%d %s", path, w.Code, w.Body)
					}
				}
				if w := request("GET", "/v1/devices", "", token, cookie); w.Code != 403 {
					t.Fatalf("devices=%d", w.Code)
				}
				w := request("POST", "/v1/providers/missing/banked-resets/redeem", `{}`, token, cookie)
				if !allow && (w.Code != 403 || !strings.Contains(w.Body.String(), "api.paired_devices.allow_reset_redemption")) {
					t.Fatalf("redemption=%d %s", w.Code, w.Body)
				}
				if allow && (w.Code == 401 || w.Code == 403) {
					t.Fatalf("enabled redemption=%d %s", w.Code, w.Body)
				}
				w = request("POST", "/v1/reset-signals/firesale", `{}`, token, cookie)
				if w.Code == 401 || w.Code == 403 {
					t.Fatalf("firesale=%d %s", w.Code, w.Body)
				}
			}
			for _, path := range []string{"/v1/providers/missing/banked-resets/redeem", "/v1/notifications/test"} {
				if w := request("POST", path, `{}`, cfg.APIToken, false); w.Code == 401 || w.Code == 403 {
					t.Fatalf("owner %s=%d %s", path, w.Code, w.Body)
				}
			}
			legacyRedeem := request("POST", "/v1/providers/missing/banked-resets/redeem", `{}`, cfg.APIToken, true)
			if !allow && (legacyRedeem.Code != 403 || !strings.Contains(legacyRedeem.Body.String(), "api.paired_devices.allow_reset_redemption")) {
				t.Fatalf("legacy redeem=%d %s", legacyRedeem.Code, legacyRedeem.Body)
			}
			if allow && (legacyRedeem.Code == 401 || legacyRedeem.Code == 403) {
				t.Fatalf("legacy enabled redeem=%d", legacyRedeem.Code)
			}
			legacy := request("GET", "/v1/tasks", "", cfg.APIToken, true)
			if legacy.Code != 200 || len(legacy.Result().Cookies()) != 1 || legacy.Result().Cookies()[0].Value == cfg.APIToken {
				t.Fatalf("legacy=%d cookies=%v", legacy.Code, legacy.Result().Cookies())
			}
			upgraded := legacy.Result().Cookies()[0].Value
			if w := request("GET", "/v1/tasks", "", upgraded, false); w.Code != 200 {
				t.Fatalf("upgraded cookie token=%d", w.Code)
			}
			if w := request("POST", "/v1/pairing", "", upgraded, false); w.Code != 403 {
				t.Fatalf("upgraded permissions=%d", w.Code)
			}
			if w := request("POST", "/v1/pairing", "", cfg.APIToken, true); w.Code != 403 {
				t.Fatalf("legacy permissions=%d", w.Code)
			}
			list := request("GET", "/v1/devices", "", cfg.APIToken, false)
			if list.Code != 200 || strings.Contains(list.Body.String(), "token_hash") || strings.Contains(list.Body.String(), token) || strings.Contains(list.Body.String(), hash) {
				t.Fatalf("list=%d %s", list.Code, list.Body)
			}
			var devices []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &devices); err != nil {
				t.Fatal(err)
			}
			for _, d := range devices {
				if w := request("DELETE", "/v1/devices/"+d.ID, "", token, false); w.Code != 403 {
					t.Fatalf("device revoke=%d", w.Code)
				}
			}
			for _, d := range devices {
				if w := request("DELETE", "/v1/devices/"+d.ID, "", cfg.APIToken, false); w.Code != 204 {
					t.Fatalf("owner revoke=%d %s", w.Code, w.Body)
				}
			}
			for _, cookie := range []bool{false, true} {
				if w := request("GET", "/v1/tasks", "", token, cookie); w.Code != 401 {
					t.Fatalf("revoked=%d", w.Code)
				}
			}
		})
	}
}
