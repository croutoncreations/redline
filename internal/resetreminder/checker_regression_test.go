package resetreminder_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/resetreminder"
	"github.com/croutoncreations/redline/internal/store"
)

func TestCheckerLateFirstObservationOnlySendsExpiryAction(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &resetNotifier{}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(30 * time.Minute)
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "credit", ExpiresAt: &expires, ResetsLeft: 1}}}
	for _, at := range []time.Time{now, now.Add(time.Minute)} {
		if err := r.Check(t.Context(), "codex-main", config.Provider{Provider: "codex"}, snapshot, at); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.events) != 1 || n.events[0].Data["action"] != "redeem_now" {
		t.Fatalf("events=%+v", n.events)
	}
}

func TestCheckerDeduplicatesAggregateAndItemizedDeadlines(t *testing.T) {
	for _, itemizedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggregate-first", true: "itemized-first"}[itemizedFirst], func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			n := &resetNotifier{}
			r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			expires := now.Add(2 * time.Hour)
			count := 2
			aggregate := decision.UsageSnapshot{BankedResets: &count, BankedResetsExpireAt: &expires}
			itemized := aggregate
			itemized.BankedResetDetails = []decision.BankedReset{
				{ID: "one", ExpiresAt: &expires, ResetsLeft: 1},
				{ID: "two", ExpiresAt: &expires, ResetsLeft: 1},
			}
			snapshots := []decision.UsageSnapshot{aggregate, itemized, aggregate}
			if itemizedFirst {
				snapshots = []decision.UsageSnapshot{itemized, aggregate, itemized}
			}
			for _, at := range []time.Time{now, now.Add(time.Hour)} {
				for _, snapshot := range snapshots {
					if err := r.Check(t.Context(), "codex-main", config.Provider{Provider: "codex"}, snapshot, at); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(n.events) != 2 || n.events[0].Data["action"] != "remind" || n.events[1].Data["action"] != "redeem_now" {
				t.Fatalf("events=%+v", n.events)
			}
			for _, event := range n.events {
				if event.Data["count"] != "2" {
					t.Fatalf("event=%+v", event)
				}
			}
		})
	}
}

func TestCheckerIgnoresStaleAndSpentDetails(t *testing.T) {
	for _, spentCount := range []int{1, 0, -1} {
		db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
		if err != nil {
			t.Fatal(err)
		}
		n := &resetNotifier{}
		r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
		now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		soon, later := now.Add(30*time.Minute), now.Add(2*time.Hour)
		count := 1
		snapshot := decision.UsageSnapshot{BankedResets: &count, BankedResetsExpireAt: &later, BankedResetDetails: []decision.BankedReset{
			{ID: "spent", ExpiresAt: &soon, ResetsLeft: spentCount},
			{ID: "remaining", ExpiresAt: &later, ResetsLeft: 1},
		}}
		if err := r.Check(t.Context(), "codex-main", config.Provider{Provider: "codex"}, snapshot, now); err != nil {
			t.Fatal(err)
		}
		if len(n.events) != 1 || n.events[0].Data["action"] != "remind" || n.events[0].Data["expires_at"] != later.Format(time.RFC3339) {
			t.Fatalf("spentCount=%d events=%+v", spentCount, n.events)
		}
		if spentCount > 0 && n.events[0].Data["reset_id"] != "aggregate" {
			t.Fatalf("stale details did not fall back: %+v", n.events)
		}
		db.Close()
	}
}
