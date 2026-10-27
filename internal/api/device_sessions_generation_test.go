package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Demo servers run without an API token, so no request has a principal. Demo
// redemption must still report its documented conflict, not a 403.
func TestDemoRedemptionReportsDisabledNotForbidden(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	s.config.APIToken = ""
	s.redemptionDisabled = true
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "http://localhost/v1/providers/main/banked-resets/redeem", strings.NewReader(`{"dry_run":true}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "demo mode") {
		t.Fatalf("demo redeem=%d %s", w.Code, w.Body)
	}
}

// Device sessions belong to the master token that issued them. Rotating the
// token (then restarting) invalidates every session, including any the old
// process minted after rotation, without the CLI having to find the database.
func TestDeviceSessionsDieWithTheIssuingMasterToken(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	token, _, err := s.newDeviceSession(httptest.NewRequest("GET", "http://localhost", nil), "Phone", "mobile")
	if err != nil {
		t.Fatal(err)
	}
	call := func(srv *Server) int {
		r := httptest.NewRequest("GET", "http://localhost/v1/tasks", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(s); code != http.StatusOK {
		t.Fatalf("before rotation=%d", code)
	}
	// Same database, same master token (e.g. a failed rotation): still valid.
	same := newServer(s.config, s.store, s.now, nil, nil, nil)
	if code := call(same); code != http.StatusOK {
		t.Fatalf("restart without rotation=%d", code)
	}
	// The service restarted with a rotated master token.
	cfg := s.config
	cfg.APIToken = "rotated-secret"
	rotated := newServer(cfg, s.store, s.now, nil, nil, nil)
	if code := call(rotated); code != http.StatusUnauthorized {
		t.Fatalf("after rotation=%d, want 401", code)
	}
}
