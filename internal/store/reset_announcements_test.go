package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetAnnouncementsPersistAndDeduplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	a := resetsignal.Announcement{Key: "announcement:123", Kind: resetsignal.KindPromised, Quote: "promise"}
	for _, kind := range []resetsignal.Kind{resetsignal.KindPromised, resetsignal.KindScheduled, resetsignal.KindCompleted} {
		a.Kind = kind
		needed, err := db.ObserveResetAnnouncement(ctx, a, now)
		if err != nil || !needed {
			t.Fatalf("first %s: %v %v", kind, needed, err)
		}
		claimed, err := db.ClaimResetAnnouncement(ctx, a, now)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		claimed, err = db.ClaimResetAnnouncement(ctx, a, now)
		if err != nil || claimed {
			t.Fatalf("repeat claim=%v err=%v", claimed, err)
		}
		needed, err = db.ObserveResetAnnouncement(ctx, a, now.Add(time.Minute))
		if err != nil || needed {
			t.Fatalf("repeat: %v %v", needed, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a.Kind = resetsignal.KindPromised
	if needed, err := db.ObserveResetAnnouncement(ctx, a, now); err != nil || needed {
		t.Fatalf("restart: %v %v", needed, err)
	}
	records, err := db.UnretractedResetAnnouncements(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	claimed, err := db.ClaimResetAnnouncementRetraction(ctx, a.Key, now)
	if err != nil || !claimed {
		t.Fatalf("retraction claim=%v err=%v", claimed, err)
	}
	claimed, err = db.ClaimResetAnnouncementRetraction(ctx, a.Key, now)
	if err != nil || claimed {
		t.Fatalf("repeat retraction claim=%v err=%v", claimed, err)
	}
	records, err = db.UnretractedResetAnnouncements(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("retracted=%+v err=%v", records, err)
	}
}

func TestResetAnnouncementsMigration38RepairsStampedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE reset_announcements; INSERT OR IGNORE INTO schema_migrations(version) VALUES(38)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.UnretractedResetAnnouncements(t.Context()); err != nil {
		t.Fatal(err)
	}
}
