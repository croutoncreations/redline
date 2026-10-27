package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

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
