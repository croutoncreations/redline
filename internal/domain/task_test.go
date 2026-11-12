package domain_test

import (
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
)

func TestDispatchTierRankOrdersByPressure(t *testing.T) {
	ordered := []domain.DispatchTier{"", domain.DispatchBehind, domain.DispatchWellBehind, domain.DispatchExpiring, domain.DispatchFiresale}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Rank() >= ordered[i].Rank() {
			t.Fatalf("%q must rank below %q", ordered[i-1], ordered[i])
		}
	}
	if got := domain.DispatchTier("bogus").Rank(); got != 0 {
		t.Fatalf("unrecognized tier rank = %d, want 0", got)
	}
}

func TestDispatchTierUnlocks(t *testing.T) {
	cases := []struct {
		unlocked, required domain.DispatchTier
		want               bool
	}{
		{domain.DispatchBehind, "", true},
		{"", "", false},
		{domain.DispatchBehind, domain.DispatchBehind, true},
		{domain.DispatchBehind, domain.DispatchWellBehind, false},
		{domain.DispatchExpiring, domain.DispatchWellBehind, true},
		{domain.DispatchExpiring, domain.DispatchFiresale, false},
		{domain.DispatchFiresale, domain.DispatchFiresale, true},
		{domain.DispatchFiresale, domain.DispatchBehind, true},
	}
	for _, tc := range cases {
		if got := tc.unlocked.Unlocks(tc.required); got != tc.want {
			t.Errorf("%q.Unlocks(%q) = %v, want %v", tc.unlocked, tc.required, got, tc.want)
		}
	}
}
