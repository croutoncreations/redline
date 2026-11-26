package apicredits

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func loadConsoleFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// consoleTranche is a synthetic Console credit tranche. amount and remaining
// are in cents.
func consoleTranche(program string, granted, remaining int, grantedAt, expiresAt string) string {
	return fmt.Sprintf(`{"remaining_amount_minor_units":%d,"currency":"USD","expires_at":%s,"shown_expires_at":null,`+
		`"granted_amount_minor_units":%d,"granted_at":%s,`+
		`"remaining":{"money":null,"credits":{"amount_minor":%d,"exponent":2}},`+
		`"granted":{"money":null,"credits":{"amount_minor":%d,"exponent":2}},`+
		`"program_id":%q,"name":"Synthetic","id":"tranche_SYNTHETIC","scope":{"kind":"scoped","applies_to":[]}}`,
		remaining, quoteOrNull(expiresAt), granted, quoteOrNull(grantedAt), remaining, granted, program)
}

func quoteOrNull(value string) string {
	if value == "" {
		return "null"
	}
	return `"` + value + `"`
}

func consoleCredits(promo []string, paid []string) []byte {
	return []byte(`{"amount":1,"currency":"USD","balance":{"money":null,"credits":{"amount_minor":1,"exponent":2}},` +
		`"tranches":[` + strings.Join(paid, ",") + `],"promo_tranches":[` + strings.Join(promo, ",") + `]}`)
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestParseConsoleCreditsReportsSubscriptionCreditAsPromo(t *testing.T) {
	report, err := ParseConsoleCredits(loadConsoleFixture(t, "console-prepaid-credits.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 62.4 || report.Promo.Granted != 100 || report.Promo.NextExpiring != 62.4 {
		t.Fatalf("promo = %+v, want 62.40 of 100", report.Promo)
	}
	wantExpiry := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if report.Promo.ExpiresAt == nil || !report.Promo.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("promo expiry = %v", report.Promo.ExpiresAt)
	}
	wantStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if report.Promo.EffectiveAt == nil || !report.Promo.EffectiveAt.Equal(wantStart) {
		t.Fatalf("promo start = %v", report.Promo.EffectiveAt)
	}
}

// Purchased tranches and other promo programs (credit_program_v2, which
// chat and other products also spend) are shown but are never capacity;
// the top-level amount, which sums every pot, is ignored.
func TestParseConsoleCreditsSeparatesPaidAndOtherPrograms(t *testing.T) {
	report, err := ParseConsoleCredits(loadConsoleFixture(t, "console-prepaid-credits.json"), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.PaidAvailable != 25.25 {
		t.Fatalf("paid available = %v, want 7.25 purchased + 18 other", report.PaidAvailable)
	}
	if report.TotalAvailable != 87.65 {
		t.Fatalf("total available = %v, want 87.65", report.TotalAvailable)
	}
	want := []struct{ kind, state string }{
		{KindPurchase, StateAvailable},
		{KindPromo, StateAvailable},
		{KindOther, StateAvailable},
		{KindPromo, StateInactive},
	}
	if len(report.Grants) != len(want) {
		t.Fatalf("grants = %+v", report.Grants)
	}
	for i, w := range want {
		if got := report.Grants[i]; got.Kind != w.kind || got.State != w.state {
			t.Errorf("grant %d = %s/%s, want %s/%s", i, got.Kind, got.State, w.kind, w.state)
		}
	}
	if report.Grants[0].ExpiresAt != nil || report.Grants[0].Remaining != 7.25 || report.Grants[0].Used != 2.75 {
		t.Fatalf("purchase = %+v", report.Grants[0])
	}
}

// Subscription credit that has lapsed, not yet started, or is used up is
// not spendable; a missing expiry is never read as "never expires".
func TestParseConsoleCreditsExcludesUnspendableSubscriptionCredit(t *testing.T) {
	start, past, future := iso(fixtureNow.Add(-24*time.Hour)), iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour))
	for name, tranche := range map[string]string{
		"expired":     consoleTranche("subscription_api_credit", 10000, 5000, start, past),
		"not started": consoleTranche("subscription_api_credit", 10000, 5000, future, iso(fixtureNow.Add(48*time.Hour))),
		"used up":     consoleTranche("subscription_api_credit", 10000, 0, start, future),
		"no expiry":   consoleTranche("subscription_api_credit", 10000, 5000, start, ""),
		"no start":    consoleTranche("subscription_api_credit", 10000, 5000, "", future),
	} {
		report, err := ParseConsoleCredits(consoleCredits([]string{tranche}, nil), fixtureNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if report.Promo.Remaining != 0 || report.Promo.ExpiresAt != nil || report.Grants[0].State != StateInactive {
			t.Errorf("%s: promo = %+v grant = %+v", name, report.Promo, report.Grants[0])
		}
	}
}

// Two subscription tranches (an upgrade mid-cycle) are summed; the expiry
// shown is the soonest, because that money lapses first.
func TestParseConsoleCreditsSumsSubscriptionTranchesAndUsesSoonestExpiry(t *testing.T) {
	start := iso(fixtureNow.Add(-24 * time.Hour))
	soon, later := fixtureNow.Add(48*time.Hour), fixtureNow.Add(96*time.Hour)
	report, err := ParseConsoleCredits(consoleCredits([]string{
		consoleTranche("subscription_api_credit", 5000, 3000, start, iso(later)),
		consoleTranche("subscription_api_credit", 10000, 7500, start, iso(soon)),
	}, nil), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if report.Promo.Remaining != 105 || report.Promo.Granted != 150 || report.Promo.NextExpiring != 75 {
		t.Fatalf("promo = %+v", report.Promo)
	}
	if !report.Promo.ExpiresAt.Equal(soon.Truncate(time.Second)) {
		t.Fatalf("expiry = %v", report.Promo.ExpiresAt)
	}
}

// Tranches that lapse together all count toward the amount expiring then,
// whatever order the Console lists them in.
func TestParseConsoleCreditsSumsSubscriptionTranchesSharingTheSoonestExpiry(t *testing.T) {
	start := iso(fixtureNow.Add(-24 * time.Hour))
	soon, later := iso(fixtureNow.Add(48*time.Hour)), iso(fixtureNow.Add(96*time.Hour))
	thirty := consoleTranche("subscription_api_credit", 5000, 3000, start, soon)
	seventyFive := consoleTranche("subscription_api_credit", 10000, 7500, start, soon)
	late := consoleTranche("subscription_api_credit", 2000, 2000, start, later)
	for name, promo := range map[string][]string{
		"forward": {thirty, late, seventyFive},
		"reverse": {seventyFive, late, thirty},
	} {
		report, err := ParseConsoleCredits(consoleCredits(promo, nil), fixtureNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if report.Promo.NextExpiring != 105 || report.Promo.Remaining != 125 {
			t.Errorf("%s: promo = %+v, want 105 of 125 expiring first", name, report.Promo)
		}
	}
}

// A subscription tranche reporting more left than it granted is not a
// balance Redline understands, so the read fails closed.
func TestParseConsoleCreditsRefusesSubscriptionRemainingAboveGranted(t *testing.T) {
	start, future := iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour))
	if _, err := ParseConsoleCredits(consoleCredits([]string{
		consoleTranche("subscription_api_credit", 10000, 20000, start, future)}, nil), fixtureNow); err == nil {
		t.Fatal("remaining above granted was accepted")
	}
	for name, remaining := range map[string]int{"partly spent": 4000, "untouched": 10000, "exhausted": 0} {
		if _, err := ParseConsoleCredits(consoleCredits([]string{
			consoleTranche("subscription_api_credit", 10000, remaining, start, future)}, nil), fixtureNow); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Amounts are amount_minor / 10^exponent; the legacy minor-units field (in
// cents) is used only when the remaining object is missing.
func TestParseConsoleCreditsMinorUnitMaths(t *testing.T) {
	start, future := iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour))
	exponent3 := `{"remaining_amount_minor_units":0,"currency":"USD","expires_at":"` + future + `","granted_at":"` + start + `",` +
		`"remaining":{"money":null,"credits":{"amount_minor":12345,"exponent":3}},` +
		`"granted":{"money":null,"credits":{"amount_minor":20000,"exponent":3}},"program_id":"subscription_api_credit"}`
	fallback := `{"remaining_amount_minor_units":4321,"granted_amount_minor_units":5000,"currency":"USD","expires_at":"` + future +
		`","granted_at":"` + start + `","program_id":"subscription_api_credit"}`
	for name, tc := range map[string]struct {
		tranche          string
		remaining, total float64
	}{
		"exponent 3":           {exponent3, 12.345, 20},
		"minor-units fallback": {fallback, 43.21, 50},
	} {
		report, err := ParseConsoleCredits(consoleCredits([]string{tc.tranche}, nil), fixtureNow)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if report.Promo.Remaining != tc.remaining || report.Promo.Granted != tc.total {
			t.Fatalf("%s: promo = %+v", name, report.Promo)
		}
	}
}

// Anything the parser does not understand makes the read fail (the account
// shows UNKNOWN) rather than being guessed at.
func TestParseConsoleCreditsFailsClosedOnUnexpectedShapes(t *testing.T) {
	start, future := iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour))
	good := consoleTranche("subscription_api_credit", 10000, 5000, start, future)
	cases := map[string][]byte{
		"not json":         []byte(`<html>`),
		"no promo list":    []byte(`{"currency":"USD","tranches":[]}`),
		"no paid list":     []byte(`{"currency":"USD","promo_tranches":[]}`),
		"euro account":     []byte(strings.Replace(string(consoleCredits([]string{good}, nil)), `"currency":"USD","balance"`, `"currency":"EUR","balance"`, 1)),
		"euro tranche":     consoleCredits([]string{strings.Replace(good, `"currency":"USD"`, `"currency":"EUR"`, 1)}, nil),
		"no program":       consoleCredits([]string{strings.Replace(good, `"program_id":"subscription_api_credit",`, ``, 1)}, nil),
		"bad expiry":       consoleCredits([]string{consoleTranche("subscription_api_credit", 10000, 5000, start, "next month")}, nil),
		"bad start":        consoleCredits([]string{consoleTranche("subscription_api_credit", 10000, 5000, "yesterday", future)}, nil),
		"money differs":    consoleCredits([]string{strings.Replace(good, `"remaining":{"money":null`, `"remaining":{"money":{"amount_minor":1,"exponent":2}`, 1)}, nil),
		"money unreadable": consoleCredits([]string{strings.Replace(good, `"remaining":{"money":null`, `"remaining":{"money":"1.00"`, 1)}, nil),
		"no credits":       consoleCredits([]string{strings.Replace(good, `"remaining":{"money":null,"credits":{"amount_minor":5000,"exponent":2}}`, `"remaining":{"money":null,"credits":null}`, 1)}, nil),
		"negative":         consoleCredits([]string{strings.Replace(good, `"amount_minor":5000`, `"amount_minor":-5000`, 1)}, nil),
		"huge exponent":    consoleCredits([]string{strings.Replace(good, `"amount_minor":5000,"exponent":2`, `"amount_minor":5000,"exponent":40`, 1)}, nil),
		"no amount at all": consoleCredits([]string{`{"currency":"USD","expires_at":"` + future + `","granted_at":"` + start + `","program_id":"subscription_api_credit"}`}, nil),
		"bad paid tranche": consoleCredits([]string{good}, []string{strings.Replace(good, `"currency":"USD"`, `"currency":"GBP"`, 1)}),
	}
	for name, body := range cases {
		if _, err := ParseConsoleCredits(body, fixtureNow); err == nil {
			t.Errorf("%s: parsed without error", name)
		} else if strings.Contains(err.Error(), "tranche_SYNTHETIC") {
			t.Errorf("%s: error echoes the body: %v", name, err)
		}
	}
}

// A money object equal to credits is consistent and accepted.
func TestParseConsoleCreditsAcceptsMatchingMoney(t *testing.T) {
	good := consoleTranche("subscription_api_credit", 10000, 5000, iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour)))
	body := consoleCredits([]string{strings.Replace(good, `"remaining":{"money":null`, `"remaining":{"money":{"amount_minor":5000,"exponent":2,"currency":"USD"}`, 1)}, nil)
	report, err := ParseConsoleCredits(body, fixtureNow)
	if err != nil || report.Promo.Remaining != 50 {
		t.Fatalf("report = %+v err = %v", report.Promo, err)
	}
}

func TestSelectConsoleOrganizationPicksTheOnlyAPIOrganization(t *testing.T) {
	orgs, err := ParseConsoleOrganizations(loadConsoleFixture(t, "console-organizations.json"))
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := SelectConsoleOrganization(orgs)
	if err != nil || uuid != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("uuid = %q err = %v", uuid, err)
	}
}

func TestSelectConsoleOrganizationNeedsExactlyOneAPIOrganization(t *testing.T) {
	none := `[{"uuid":"11111111-1111-4111-8111-111111111111","capabilities":["chat"]}]`
	several := `[{"uuid":"11111111-1111-4111-8111-111111111111","capabilities":["api"]},{"uuid":"22222222-2222-4222-8222-222222222222","capabilities":["api"]}]`
	orgs, err := ParseConsoleOrganizations([]byte(none))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectConsoleOrganization(orgs); err != ErrNoConsoleAPIOrganization {
		t.Fatalf("none: err = %v", err)
	}
	orgs, err = ParseConsoleOrganizations([]byte(several))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectConsoleOrganization(orgs); err != ErrSeveralConsoleOrganizations ||
		err.Error() != "several Console organizations have API access; set organization_id" {
		t.Fatalf("several: err = %v", err)
	}
}

// An organization UUID is the only part of the URL that comes from data, so
// a malformed one is refused rather than spliced into a path.
func TestSelectConsoleOrganizationRefusesMalformedUUID(t *testing.T) {
	orgs, err := ParseConsoleOrganizations([]byte(`[{"uuid":"../../steal","capabilities":["api"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectConsoleOrganization(orgs); err == nil {
		t.Fatal("selected a malformed uuid")
	}
	if _, err := ParseConsoleOrganizations([]byte(`{"error":"x"}`)); err == nil {
		t.Fatal("parsed a non-list")
	}
}

// A money object in another currency is not the same amount, whatever its
// digits; one without a currency, or in USD, is accepted.
func TestParseConsoleCreditsRequiresUSDMoney(t *testing.T) {
	good := consoleTranche("subscription_api_credit", 10000, 5000, iso(fixtureNow.Add(-time.Hour)), iso(fixtureNow.Add(time.Hour)))
	withMoney := func(money string) []byte {
		return consoleCredits([]string{strings.Replace(good, `"remaining":{"money":null`, `"remaining":{"money":`+money, 1)}, nil)
	}
	for _, money := range []string{`{"amount_minor":5000,"exponent":2,"currency":"EUR"}`, `{"amount_minor":5000,"exponent":2,"currency":""}`,
		`{"amount_minor":5000,"exponent":2,"currency":7}`} {
		if _, err := ParseConsoleCredits(withMoney(money), fixtureNow); err == nil {
			t.Errorf("money %s: parsed without error", money)
		}
	}
	for _, money := range []string{`{"amount_minor":5000,"exponent":2,"currency":"usd"}`, `{"amount_minor":5000,"exponent":2}`} {
		if report, err := ParseConsoleCredits(withMoney(money), fixtureNow); err != nil || report.Promo.Remaining != 50 {
			t.Errorf("money %s: report = %+v err = %v", money, report.Promo, err)
		}
	}
}

// The Console's live timestamps are RFC 3339 in UTC with microseconds on
// granted_at, for example 2026-11-01T12:34:56.123456Z.
func TestParseConsoleCreditsReadsLiveTimestampFormat(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	tranche := consoleTranche("subscription_api_credit", 10000, 5000, "2026-11-01T12:34:56.123456Z", "2026-12-01T00:00:00Z")
	report, err := ParseConsoleCredits(consoleCredits([]string{tranche}, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 11, 1, 12, 34, 56, 123456000, time.UTC)
	if report.Promo.EffectiveAt == nil || !report.Promo.EffectiveAt.Equal(wantStart) || report.Promo.Remaining != 50 {
		t.Fatalf("promo = %+v", report.Promo)
	}
}
