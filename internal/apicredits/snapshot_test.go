package apicredits

import (
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

func fetched(t *testing.T) FetchedReport {
	t.Helper()
	report, err := Parse(loadFixture(t), fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	at := fixtureNow
	return FetchedReport{Report: report, FetchedAt: &at}
}

// The promo balance becomes the account window: remaining is a fraction of
// the grant, resetting when it expires, over the grant's own period.
func TestSnapshotMapsPromoToAccountWindow(t *testing.T) {
	snapshot, err := Snapshot(fetched(t), config.EffectiveAPICredits{MinPromoBalance: 5})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != config.ProviderAPICredits || !snapshot.ObservedAt.Equal(fixtureNow) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if got := snapshot.Weekly.Remaining; got < 0.9166 || got > 0.9167 {
		t.Fatalf("remaining = %v, want 110/120", got)
	}
	if !snapshot.Weekly.ResetsAt.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", snapshot.Weekly.ResetsAt)
	}
	if snapshot.Weekly.PeriodSeconds != int64(31*24*time.Hour/time.Second) {
		t.Fatalf("period = %d", snapshot.Weekly.PeriodSeconds)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if snapshot.Credits == nil || snapshot.Credits.PromoRemaining != 110 || snapshot.Credits.PaidAvailable != 7.25 {
		t.Fatalf("credits = %+v", snapshot.Credits)
	}
	if snapshot.Credits.MinPromoBalance != 5 {
		t.Fatalf("floor = %v", snapshot.Credits.MinPromoBalance)
	}
}

// With no promotional credit to spend there is nothing to schedule, but the
// account still reports: an empty balance is a number, not missing data.
func TestSnapshotWithoutPromoReportsEmptyBalance(t *testing.T) {
	report := fetched(t)
	report.Promo = PromoBalance{}
	snapshot, err := Snapshot(report, config.EffectiveAPICredits{})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.WeeklyUnavailable || snapshot.Credits == nil || snapshot.Credits.PromoRemaining != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRequiresAnObservation(t *testing.T) {
	if _, err := Snapshot(FetchedReport{}, config.EffectiveAPICredits{}); err == nil || !strings.Contains(err.Error(), "not been read") {
		t.Fatalf("err = %v", err)
	}
}

// The raw payload stored with each snapshot carries the derived report,
// never grant IDs.
func TestSnapshotRawPayloadHasNoGrantIDs(t *testing.T) {
	raw, err := RawPayload(fetched(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"chatgpt_api_credits_promo_", "auto_recharge_grant_", "in_TEST"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("raw payload contains %q", leaked)
		}
	}
}
