package resetreminder_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetreminder"
	"github.com/croutoncreations/redline/internal/store"
)

type resetNotifier struct {
	events   []domain.NotificationEvent
	err      error
	onNotify func()
}

func (n *resetNotifier) Notify(_ context.Context, e domain.NotificationEvent) error {
	if n.onNotify != nil {
		n.onNotify()
	}
	n.events = append(n.events, e)
	return n.err
}

func TestBankedResetRemindersSkipsMissedLeads(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &resetNotifier{}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(30 * time.Minute)
	p := config.Provider{Provider: "claude", BankedResets: config.BankedResets{RemindBefore: []string{"1h", "24h", "15m", "48h"}, OnExpiry: "off"}}
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 1}}}
	for _, at := range []time.Time{now, now.Add(time.Minute), now.Add(15 * time.Minute), expires, expires.Add(time.Minute)} {
		if err := r.Check(t.Context(), "claude-main", p, snapshot, at); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.events) != 2 || n.events[0].Data["lead"] != "1h" || n.events[1].Data["lead"] != "15m" {
		t.Fatalf("events=%+v", n.events)
	}
}

func TestBankedResetRemindersExpiryAction(t *testing.T) {
	for _, action := range []string{"notify", "off"} {
		t.Run(action, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			n := &resetNotifier{}
			r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			expires := now.Add(time.Hour)
			p := config.Provider{Provider: "claude", BankedResets: config.BankedResets{RemindBefore: []string{}, OnExpiry: action}}
			snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 1}}}
			for _, at := range []time.Time{now.Add(-time.Nanosecond), now, now.Add(time.Minute)} {
				if err := r.Check(t.Context(), "claude-main", p, snapshot, at); err != nil {
					t.Fatal(err)
				}
			}
			if action == "off" {
				if len(n.events) != 0 {
					t.Fatalf("events=%+v", n.events)
				}
				return
			}
			if len(n.events) != 1 {
				t.Fatalf("events=%+v", n.events)
			}
			e := n.events[0]
			if e.Data["action"] != "redeem_now" || e.Data["lead"] != "1h" || e.Message != "Claude banked reset expires in 1h — redeem it now" {
				t.Fatalf("event=%+v", e)
			}
		})
	}
}

func TestBankedResetRedeemModeRemindersOnlyBeforeAction(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &resetNotifier{}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n, AutoRedeems: func(decision.BankedReset) bool { return true }}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Hour)
	p := config.Provider{Provider: "claude", BankedResets: config.BankedResets{OnExpiry: "redeem", RemindBefore: []string{"24h", "1h"}}}
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 1}}}
	for _, at := range []time.Time{now, now.Add(time.Hour), now.Add(90 * time.Minute)} {
		if err := r.Check(t.Context(), "main", p, snapshot, at); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.events) != 1 || n.events[0].Data["action"] != "remind" || n.events[0].Data["lead"] != "24h" {
		t.Fatalf("events=%+v", n.events)
	}
}

func TestBankedResetRemindersAggregateFallback(t *testing.T) {
	for _, itemized := range []bool{false, true} {
		t.Run(fmt.Sprint(itemized), func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			n := &resetNotifier{}
			r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
			now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
			expires := now.Add(2 * time.Hour)
			count := 3
			snapshot := decision.UsageSnapshot{BankedResets: &count, BankedResetsExpireAt: &expires}
			id, wantCount := "aggregate", "3"
			if itemized {
				count = 1
				snapshot.BankedResetDetails = []decision.BankedReset{{ID: "credit", ExpiresAt: &expires, ResetsLeft: 1}}
				id, wantCount = "credit", "1"
			}
			if err := r.Check(t.Context(), "codex-main", config.Provider{Provider: "codex"}, snapshot, now); err != nil {
				t.Fatal(err)
			}
			if len(n.events) != 1 || n.events[0].Data["reset_id"] != id || n.events[0].Data["count"] != wantCount {
				t.Fatalf("events=%+v", n.events)
			}
		})
	}
}

func TestBankedResetRemindersDisabledDoesNotClaimNotices(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &resetNotifier{}
	r := resetreminder.Checker{Store: db, Notifier: n}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Hour)
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 1}}}
	p := config.Provider{Provider: "claude"}
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err != nil {
		t.Fatal(err)
	}
	r.Enabled, r.Notifier = true, nil
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err != nil {
		t.Fatal(err)
	}
	r.Notifier = n
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 {
		t.Fatalf("events=%+v", n.events)
	}
}

func TestBankedResetRemindersClaimsBeforeNotifyAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notices.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	n := &resetNotifier{err: fmt.Errorf("hook failed")}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(2 * time.Hour)
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 1}}}
	p := config.Provider{Provider: "claude"}
	n.onNotify = func() {
		inserted, err := db.RecordBankedResetNotice(t.Context(), "claude-main", "grant", expires, "remind:24h", now)
		if err != nil || inserted {
			t.Fatalf("notice not claimed before notification: inserted=%v err=%v", inserted, err)
		}
	}
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err == nil {
		t.Fatal("missing notification error")
	}
	inserted, err := db.RecordBankedResetNotice(t.Context(), "claude-main", "grant", expires, "remind:24h", now)
	if err != nil || inserted {
		t.Fatalf("notice was not claimed: inserted=%v err=%v", inserted, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r.Store = db
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 1 {
		t.Fatalf("duplicate events=%+v", n.events)
	}
}

func TestBankedResetRemindersIgnoresUnknownAndExpiredDeadlines(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := &resetNotifier{}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: n}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{
		{ID: "unknown", ResetsLeft: 1}, {ID: "expired", ExpiresAt: &past, ResetsLeft: 1}, {ID: "now", ExpiresAt: &now, ResetsLeft: 1},
	}}
	if err := r.Check(t.Context(), "claude-main", config.Provider{}, snapshot, now); err != nil {
		t.Fatal(err)
	}
	if len(n.events) != 0 {
		t.Fatalf("events=%+v", n.events)
	}
}

func TestBankedResetRemindersDeadlineAndPayload(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	notifier := &resetNotifier{}
	r := resetreminder.Checker{Enabled: true, Store: db, Notifier: notifier}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)
	p := config.Provider{Provider: "claude", BankedResets: config.BankedResets{OnExpiry: "off"}}
	snapshot := decision.UsageSnapshot{BankedResetDetails: []decision.BankedReset{{ID: "grant", ExpiresAt: &expires, ResetsLeft: 2}}}
	if err := r.Check(t.Context(), "claude-main", p, snapshot, now.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 0 {
		t.Fatal("notified before deadline")
	}
	for range 2 {
		if err := r.Check(t.Context(), "claude-main", p, snapshot, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifier.events) != 1 {
		t.Fatalf("events=%+v", notifier.events)
	}
	e := notifier.events[0]
	if e.Version != 1 || e.Type != domain.EventResetExpiring || e.ProviderAccountID != "claude-main" || !e.OccurredAt.Equal(now) || e.Message != "Claude banked reset expires in 24h" {
		t.Fatalf("event=%+v", e)
	}
	for key, want := range map[string]string{"reset_id": "grant", "expires_at": expires.Format(time.RFC3339), "lead": "24h", "action": "remind", "count": "2"} {
		if e.Data[key] != want {
			t.Fatalf("data[%s]=%q want %q", key, e.Data[key], want)
		}
	}
}
