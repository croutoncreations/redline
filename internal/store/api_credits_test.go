package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

// An API credits snapshot keeps its dollar balance, settings and grant
// period across a reload, whether read as the latest or in a list, and
// with or without promotional credit.
func TestAPICreditsSnapshotRoundTrips(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	start, expiry := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	credits := &decision.CreditsBalance{
		PromoRemaining: 110, PromoGranted: 120, NextExpiring: 110, EffectiveAt: &start, ExpiresAt: &expiry,
		PaidAvailable: 7.25, MinPromoBalance: 5, Pacing: "firesale", FiresaleWindowSeconds: 259200,
		Grants: []decision.CreditGrant{{Kind: "promo", Amount: 120, Remaining: 110, EffectiveAt: &start, ExpiresAt: &expiry}},
	}
	withPromo := decision.UsageSnapshot{
		Provider: "openai-api-credits", Account: "api-credits", ObservedAt: at, Source: "native", Confidence: "high",
		Weekly:  decision.UsageWindow{Remaining: 110.0 / 120, ResetsAt: expiry, PeriodSeconds: int64(expiry.Sub(start) / time.Second)},
		Credits: credits,
	}
	empty := decision.UsageSnapshot{
		Provider: "openai-api-credits", Account: "api-credits", ObservedAt: at.Add(time.Minute), Source: "native",
		WeeklyUnavailable: true, Credits: &decision.CreditsBalance{PaidAvailable: 3, MinPromoBalance: 5},
	}
	if err := db.SaveSnapshot(ctx, withPromo, nil); err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListSnapshots(ctx, "api-credits", "openai-api-credits", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %v, %v", listed, err)
	}
	latest, _, err := db.LatestSnapshotFromSource(ctx, "api-credits", "openai-api-credits", "native")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]decision.UsageSnapshot{"latest": latest, "list": listed[0]} {
		if !reflect.DeepEqual(got.Credits, credits) {
			t.Fatalf("%s credits = %+v", name, got.Credits)
		}
		if got.Weekly.PeriodSeconds != withPromo.Weekly.PeriodSeconds {
			t.Fatalf("%s period = %d, want %d", name, got.Weekly.PeriodSeconds, withPromo.Weekly.PeriodSeconds)
		}
	}
	if err := db.SaveSnapshot(ctx, empty, nil); err != nil {
		t.Fatal(err)
	}
	latest, _, err = db.LatestSnapshot(ctx, "api-credits", "openai-api-credits")
	if err != nil || !latest.WeeklyUnavailable || latest.Credits == nil || latest.Credits.PaidAvailable != 3 {
		t.Fatalf("empty = %+v, %v", latest, err)
	}
}
