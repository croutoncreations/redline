package store_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

func TestLatestAutomaticPressureFiresaleTreatsManualSalesAsEnded(t *testing.T) {
	db := openTaskDB(t)
	for _, raw := range []string{`{"trigger":"automatic","provider_firesale":true}`, `{"trigger":"automatic","provider_firesale":true,"result":{"firesale_trigger":"manual"}}`} {
		if _, err := db.RecordSchedulerDecision(t.Context(), domain.SchedulerDecision{ProviderAccountID: "main", DecisionJSON: []byte(raw)}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := db.LatestAutomaticPressureFiresale(t.Context(), "main"); err != nil || got {
		t.Fatalf("pressure=%v err=%v", got, err)
	}
}
