package apicredits

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func promoGrant(amount, used float64, effective, expires string, flags string) string {
	return fmt.Sprintf(`{"id":"chatgpt_api_credits_promo_x","grant_amount":%v,"used_amount":%v,"effective_at":%s,"expires_at":%s%s}`,
		amount, used, effective, expires, flags)
}

func summary(grants ...string) []byte {
	return []byte(`{"object":"credit_summary","total_available":0,"total_paid_available":0,"grants":{"object":"list","data":[` +
		strings.Join(grants, ",") + `]}}`)
}

func unix(t time.Time) string { return fmt.Sprint(t.Unix()) }

// fixtureNow is inside the synthetic promo grant's cycle (2026-01-01 to
// 2026-02-01 UTC).
var fixtureNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func loadFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/credit-grants.json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseReportsPromoBalanceAndExpiry(t *testing.T) {
	report, err := Parse(loadFixture(t), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 110 || report.Promo.Granted != 120 {
		t.Fatalf("promo = %+v, want 110 of 120 remaining", report.Promo)
	}
	wantExpiry := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if report.Promo.ExpiresAt == nil || !report.Promo.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("promo expiry = %v, want %v", report.Promo.ExpiresAt, wantExpiry)
	}
	wantStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if report.Promo.EffectiveAt == nil || !report.Promo.EffectiveAt.Equal(wantStart) {
		t.Fatalf("promo start = %v, want %v", report.Promo.EffectiveAt, wantStart)
	}
}

// Purchased and auto-recharge credit is shown but is never promo capacity.
func TestParseSeparatesPaidBalance(t *testing.T) {
	report, err := Parse(loadFixture(t), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.PaidAvailable != 7.25 {
		t.Fatalf("paid available = %v, want 7.25", report.PaidAvailable)
	}
	if report.TotalAvailable != 117.25 {
		t.Fatalf("total available = %v, want 117.25", report.TotalAvailable)
	}
}

// Each grant is classified the way the platform's Credit grants page shows
// it: Available while it has money left, an expiry and is in effect;
// Inactive otherwise.
func TestParseClassifiesGrants(t *testing.T) {
	report, err := Parse(loadFixture(t), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind  string
		state string
	}{
		{KindPromo, StateAvailable},
		{KindAutoRecharge, StateAvailable},
		{KindAutoRecharge, StateAvailable},
		{KindPromo, StateInactive},
		{KindPurchase, StateInactive},
	}
	if len(report.Grants) != len(want) {
		t.Fatalf("grants = %d, want %d", len(report.Grants), len(want))
	}
	for i, w := range want {
		if got := report.Grants[i]; got.Kind != w.kind || got.State != w.state {
			t.Errorf("grant %d = %s/%s, want %s/%s", i, got.Kind, got.State, w.kind, w.state)
		}
	}
	// A used-up grant with a fractional timestamp keeps its start time.
	used := report.Grants[3]
	if used.EffectiveAt == nil || used.EffectiveAt.Unix() != 1764547200 || used.ExpiresAt != nil {
		t.Fatalf("used promo = %+v", used)
	}
}

// Grants that have expired, have not started, or that OpenAI marks scheduled
// or held are not spendable even when they report money left.
func TestParseExcludesGrantsThatAreNotYetOrNoLongerSpendable(t *testing.T) {
	past, future := fixtureNow.Add(-time.Hour), fixtureNow.Add(time.Hour)
	start := fixtureNow.Add(-24 * time.Hour)
	cases := map[string]string{
		"expired":     promoGrant(100, 0, unix(start), unix(past), ""),
		"not started": promoGrant(100, 0, unix(future), unix(future.Add(time.Hour)), ""),
		"scheduled":   promoGrant(100, 0, unix(start), unix(future), `,"is_scheduled":true`),
		"held":        promoGrant(100, 0, unix(start), unix(future), `,"is_held_for_prepaid_transition":true`),
	}
	for name, grant := range cases {
		report, err := Parse(summary(grant), fixtureNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if report.Promo.Remaining != 0 || report.Promo.ExpiresAt != nil {
			t.Errorf("%s: promo = %+v, want none", name, report.Promo)
		}
		if report.Grants[0].State != StateInactive {
			t.Errorf("%s: state = %s, want inactive", name, report.Grants[0].State)
		}
	}
}

// Several available promo grants (an upgrade mid-cycle) are summed; the
// expiry shown is the soonest one, because that money lapses first.
func TestParseSumsPromoGrantsAndUsesSoonestExpiry(t *testing.T) {
	start := fixtureNow.Add(-24 * time.Hour)
	soon, later := fixtureNow.Add(48*time.Hour), fixtureNow.Add(96*time.Hour)
	report, err := Parse(summary(
		promoGrant(50, 20, unix(start), unix(later), ""),
		promoGrant(100, 25, unix(start), unix(soon), ""),
	), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 105 || report.Promo.Granted != 150 {
		t.Fatalf("promo = %+v, want 105 of 150", report.Promo)
	}
	if !report.Promo.ExpiresAt.Equal(soon.Truncate(time.Second)) {
		t.Fatalf("expiry = %v, want %v", report.Promo.ExpiresAt, soon)
	}
	if report.Promo.NextExpiring != 75 {
		t.Fatalf("next expiring = %v, want 75 (the soonest grant's remainder)", report.Promo.NextExpiring)
	}
}

func TestParseRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     `<html>`,
		"no grants":    `{"object":"credit_summary"}`,
		"wrong object": `{"object":"error","grants":{"data":[]}}`,
	} {
		if _, err := Parse([]byte(body), fixtureNow); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestParseIgnoresUsageAboveAmount(t *testing.T) {
	report, err := Parse(summary(promoGrant(10, 12, unix(fixtureNow.Add(-time.Hour)), unix(fixtureNow.Add(time.Hour)), "")), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Grants[0].Remaining != 0 || report.Promo.Remaining != 0 {
		t.Fatalf("overused grant = %+v", report.Grants[0])
	}
}
