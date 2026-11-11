package decision_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

func TestApplyBankedResetDetails(t *testing.T) {
	now := time.Now().UTC()
	s := decision.UsageSnapshot{Provider: "claude", ObservedAt: now, Weekly: decision.UsageWindow{ResetsAt: now.Add(time.Hour)}}
	details := []decision.BankedReset{{ID: "grant", ResetsLeft: 2, Claimable: true, Clears: []string{"five_hour", "seven_day", "seven_day_overage_included", "unknown"}}}
	s.ApplyBankedResets(&decision.BankedResets{Available: 2, Details: details})
	if len(s.BankedResetDetails) != 1 || !s.BankedResetDetails[0].Claimable || !reflect.DeepEqual(s.BankedResetDetails[0].Clears, []string{"five_hour", "weekly", "fable"}) {
		t.Fatalf("details = %#v", s.BankedResetDetails)
	}
	details[0].ID = "changed"
	if s.BankedResetDetails[0].ID != "grant" {
		t.Fatal("details were not copied")
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.ApplyBankedResets(nil)
	if s.BankedResetDetails != nil {
		t.Fatal("nil report left details behind")
	}
}

func TestApplyBankedResetsPreservesBothDeadlineReports(t *testing.T) {
	soon, later := time.Now().UTC(), time.Now().UTC().Add(time.Hour)
	details := []decision.BankedReset{{ID: "later", ResetsLeft: 1, ExpiresAt: &later}, {ID: "soon", ResetsLeft: 1, ExpiresAt: &soon}}
	for _, expiries := range [][]time.Time{nil, {later, soon}} {
		var snapshot decision.UsageSnapshot
		snapshot.ApplyBankedResets(&decision.BankedResets{Available: 2, Expiries: expiries, Details: details})
		if !reflect.DeepEqual(snapshot.BankedResetsExpiries, []time.Time{soon, later}) || snapshot.BankedResetsExpireAt == nil || !snapshot.BankedResetsExpireAt.Equal(soon) || len(snapshot.BankedResetDetails) != 2 {
			t.Fatalf("lost reset telemetry: %+v", snapshot)
		}
		snapshot.ApplyBankedResets(nil)
		if snapshot.BankedResets != nil || snapshot.BankedResetsExpireAt != nil || len(snapshot.BankedResetsExpiries) != 0 || len(snapshot.BankedResetDetails) != 0 {
			t.Fatalf("nil report left reset telemetry: %+v", snapshot)
		}
	}
}

// A Fable reset clears the 5-hour window and the Fable allowance
// (seven_day_overage_included) but not the week. Mapping that allowance to
// "weekly" would treat it as a weekly reset when scheduling and verifying.
func TestFableResetDoesNotClearTheWeek(t *testing.T) {
	var s decision.UsageSnapshot
	s.ApplyBankedResets(&decision.BankedResets{Available: 1, Details: []decision.BankedReset{
		{ID: "fable", Kind: "fable", ResetsLeft: 1, Clears: []string{"five_hour", "seven_day_overage_included"}}}})
	if got := s.BankedResetDetails[0].Clears; !reflect.DeepEqual(got, []string{"five_hour", "fable"}) {
		t.Fatalf("clears = %v", got)
	}
}

func TestBankedResetDetailsValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, detail := range []decision.BankedReset{{ResetsLeft: 1}, {ID: "grant", ResetsLeft: -1}} {
		s := decision.UsageSnapshot{Provider: "claude", ObservedAt: now, Weekly: decision.UsageWindow{ResetsAt: now.Add(time.Hour)}, BankedResetDetails: []decision.BankedReset{detail}}
		if s.Validate() == nil {
			t.Fatalf("accepted invalid detail: %#v", detail)
		}
	}
}
