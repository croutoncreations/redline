package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A phone paired before device sessions holds the master token. It trades it
// for a scoped device session in place, without re-pairing.
func TestOwnerCredentialExchangesForDeviceSession(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	call := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/v1/devices/session", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := call(s.config.APIToken, `{"name":"Pixel 9\u0007","kind":"mobile"}`)
	if w.Code != http.StatusCreated || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange=%d cache=%q %s", w.Code, w.Header().Get("Cache-Control"), w.Body)
	}
	var got struct {
		Token  string `json:"token"`
		Device struct {
			ID, Name, Kind string
		} `json:"device"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Token == "" || got.Token == s.config.APIToken {
		t.Fatalf("response=%s err=%v", w.Body, err)
	}
	if got.Device.Name != "Pixel 9" || got.Device.Kind != "mobile" || got.Device.ID == "" {
		t.Fatalf("device=%+v", got.Device)
	}
	// The new credential is a device: it can register for push but cannot mint more.
	push := httptest.NewRequest("PUT", "http://localhost/v1/push/registration", strings.NewReader(`{"platform":"android","provider":"fcm","token":"t"}`))
	push.Header.Set("Authorization", "Bearer "+got.Token)
	pw := httptest.NewRecorder()
	s.ServeHTTP(pw, push)
	if pw.Code != http.StatusNoContent && pw.Code != http.StatusOK {
		t.Fatalf("device push registration=%d %s", pw.Code, pw.Body)
	}
	if w := call(got.Token, `{"name":"again","kind":"mobile"}`); w.Code != http.StatusForbidden {
		t.Fatalf("device minted a session: %d", w.Code)
	}
	if w := call(s.config.APIToken, `{"kind":"tablet"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind=%d", w.Code)
	}
	if w := call("wrong", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", w.Code)
	}
}
