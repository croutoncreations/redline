package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
	_ "modernc.org/sqlite"
)

func TestPrimerSchemaIsEnsuredEvenOnDatabasesStampedByNewerBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Another build stamped a higher version and never created these
	// tables; a half-created schema (one table missing) must heal too.
	for _, statement := range []string{
		`DROP TABLE primer_settings`,
		`INSERT INTO schema_migrations(version) VALUES (40)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.PrimerSettings(context.Background(), "claude-main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("primer schema was not recreated: %v", err)
	}
}

func TestPrimerSettingsRoundTripAndSlotsClaimOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.PrimerSettings(ctx, "claude-main"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsaved settings err=%v", err)
	}
	now := time.Date(2026, 9, 29, 6, 1, 0, 0, time.UTC)
	want := domain.PrimerSettings{ProviderAccountID: "claude-main", Enabled: true, Mode: domain.PrimerSchedule,
		Times: []string{"06:00"}, Days: []string{"mon"}, Timezone: "UTC", Prompt: "ok", Model: "haiku",
		CatchUpSeconds: 60, UpdatedAt: now}
	if err := db.SavePrimerSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.PrimerSettings(ctx, "claude-main")
	if err != nil || !got.Enabled || got.Times[0] != "06:00" || got.Days[0] != "mon" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	attempt := domain.PrimerAttempt{ProviderAccountID: "claude-main", Trigger: "schedule", SlotKey: "schedule:x",
		TargetAt: now, Outcome: domain.PrimerRunning, StartedAt: now}
	id, err := db.ClaimPrimerSlot(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimPrimerSlot(ctx, attempt); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second claim err=%v", err)
	}
	if err := db.FailInterruptedPrimerAttempts(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	attempts, err := db.ListPrimerAttempts(ctx, "claude-main", 5)
	if err != nil || len(attempts) != 1 || attempts[0].ID != id || !attempts[0].Interrupted() {
		t.Fatalf("attempts=%#v err=%v", attempts, err)
	}
	// A ping that finishes after a restart already failed it cannot
	// overwrite the interrupted verdict.
	attempt.ID, attempt.Outcome, attempt.CompletedAt = id, domain.PrimerFired, now.Add(2*time.Minute)
	if err := db.FinishPrimerAttempt(ctx, attempt); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("finish after interruption err=%v, want ErrConflict", err)
	}
}

func TestPrimerVerificationIsWrittenOnceAndOldAttemptsArePruned(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 6, 1, 0, 0, time.UTC)
	attempt := domain.PrimerAttempt{ProviderAccountID: "claude-main", Trigger: "schedule", SlotKey: "schedule:x",
		TargetAt: now, Outcome: domain.PrimerRunning, StartedAt: now}
	id, err := db.ClaimPrimerSlot(ctx, attempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.ID, attempt.Outcome, attempt.Verification, attempt.CompletedAt = id, domain.PrimerFired, domain.PrimerVerifyPending, now.Add(time.Second)
	if err := db.FinishPrimerAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingPrimerVerifications(ctx, "claude-main")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	reset := now.Add(5 * time.Hour)
	if err := db.VerifyPrimerAttempt(ctx, id, domain.PrimerVerified, &reset, "Window opened.", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyPrimerAttempt(ctx, id, domain.PrimerVerifyUnverified, nil, "late", now.Add(3*time.Minute)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second verdict err=%v, want ErrConflict", err)
	}
	// Pruning keeps anything newer than the cutoff or still pending.
	if err := db.PrunePrimerAttempts(ctx, now); err != nil {
		t.Fatal(err)
	}
	if attempts, _ := db.ListPrimerAttempts(ctx, "claude-main", 5); len(attempts) != 1 {
		t.Fatalf("a settled attempt newer than the cutoff was pruned: %#v", attempts)
	}
	if err := db.PrunePrimerAttempts(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if attempts, _ := db.ListPrimerAttempts(ctx, "claude-main", 5); len(attempts) != 0 {
		t.Fatalf("old attempt was not pruned: %#v", attempts)
	}
}
