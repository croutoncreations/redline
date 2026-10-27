package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The device name comes from an unauthenticated request and shows in the
// owner's device list, so it is bounded and stripped of control characters.
func TestPairingRedeemBoundsDeviceName(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	mint := httptest.NewRequest("POST", "http://localhost/v1/pairing", nil)
	mint.Header.Set("Authorization", "Bearer "+s.config.APIToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, mint)
	var pairing struct {
		Token string `json:"pairing_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pairing); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("A", 500) + "\x07"
	body, _ := json.Marshal(map[string]string{"pairing_token": pairing.Token, "name": name, "kind": "mobile"})
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/v1/pairing/redeem", strings.NewReader(string(body))))
	if w.Code != http.StatusNoContent {
		t.Fatalf("redeem=%d %s", w.Code, w.Body)
	}
	devices, err := s.store.DeviceSessions(t.Context())
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices=%d err=%v", len(devices), err)
	}
	if got := devices[0].Name; len([]rune(got)) > 100 || strings.ContainsRune(got, '\x07') {
		t.Fatalf("device name not bounded: %d runes", len([]rune(got)))
	}
}

// A dashboard fires many requests at once with its old master-token cookie
// before any upgraded cookie comes back. They must share one migrated device
// session rather than flood the device list with one entry per request.
func TestLegacyCookieUpgradeReusesOneDeviceSession(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	var wg sync.WaitGroup
	cookies := make([]string, 8)
	for i := range cookies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "http://localhost/v1/tasks", nil)
			r.AddCookie(&http.Cookie{Name: apiSessionCookie, Value: s.config.APIToken})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("legacy request=%d %s", w.Code, w.Body)
				return
			}
			for _, c := range w.Result().Cookies() {
				if c.Name == apiSessionCookie {
					cookies[i] = c.Value
				}
			}
		}()
	}
	wg.Wait()
	devices, err := s.store.DeviceSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("legacy upgrade created %d device sessions, want 1", len(devices))
	}
	for _, c := range cookies {
		if c == "" || c == s.config.APIToken || c != cookies[0] {
			t.Fatalf("upgraded cookies differ or leak the master token: %q", cookies)
		}
	}
}
