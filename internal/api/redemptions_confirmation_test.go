package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
)

func TestDemoServerRefusesRealAndDryRunRedemptionWithoutCredentials(t *testing.T) {
	template, db, pre := redemptionServer(t, "claude")
	s := NewDemoServer(template.config, db, template.now, map[string]decision.UsageSnapshot{"main": pre}, nil, nil)
	retainedCredentials := s.redeemer != nil
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		t.Fatal("demo must never invoke the redeemer")
		return nativeusage.RedeemRequest{}
	}}
	for _, dryRun := range []bool{false, true} {
		body := `{"dry_run":false}`
		if dryRun {
			body = `{"dry_run":true}`
		}
		r := httptest.NewRequest("POST", "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "banked reset redemption is disabled in demo mode") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	if retainedCredentials {
		t.Fatal("demo server retained real credential-backed redeemer")
	}
	rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestBankedResetRedeemRejectsChangedConfirmedID(t *testing.T) {
	s, db, _ := redemptionServer(t, "claude")
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		t.Fatal("must not prepare or send")
		return nativeusage.RedeemRequest{}
	}}
	r := httptest.NewRequest("POST", "http://127.0.0.1:7436/v1/providers/main/banked-resets/redeem", strings.NewReader(`{"dry_run":false,"expected_reset_id":"confirmed"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "confirmed") || !strings.Contains(w.Body.String(), "reset") || !strings.Contains(w.Body.String(), "re-run to confirm") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows, err := db.BankedResetRedemptions(t.Context(), "main", 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}
