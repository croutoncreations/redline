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
	if len(s.BankedResetDetails) != 1 || !s.BankedResetDetails[0].Claimable || !reflect.DeepEqual(s.BankedResetDetails[0].Clears, []string{"five_hour", "weekly"}) {
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

func TestBankedResetDetailsValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, detail := range []decision.BankedReset{{ResetsLeft: 1}, {ID: "grant", ResetsLeft: -1}} {
		s := decision.UsageSnapshot{Provider: "claude", ObservedAt: now, Weekly: decision.UsageWindow{ResetsAt: now.Add(time.Hour)}, BankedResetDetails: []decision.BankedReset{detail}}
		if s.Validate() == nil {
			t.Fatalf("accepted invalid detail: %#v", detail)
		}
	}
}
