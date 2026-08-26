package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/store"
)

func TestTokenObservationsAreIdempotentAndQueryableByProvider(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observed := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	items := []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost", SourceID: "session:1", ObservedAt: observed, Model: "opus", InputTokens: 100, OutputTokens: 20, Confidence: "medium"},
		{Provider: "codex", Source: "gatepost", SourceID: "session:2", ObservedAt: observed, InputTokens: 999},
	}
	inserted, err := db.SaveTokenObservations(context.Background(), items)
	if err != nil || inserted != 2 {
		t.Fatalf("first save inserted=%d err=%v", inserted, err)
	}
	inserted, err = db.SaveTokenObservations(context.Background(), items)
	if err != nil || inserted != 0 {
		t.Fatalf("second save inserted=%d err=%v", inserted, err)
	}
	got, err := db.ListTokenObservations(context.Background(), "claude", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "session:1" || got[0].InputTokens != 100 {
		t.Fatalf("observations = %#v", got)
	}
}

func TestTokenObservationRequiresStableIdentityAndNonnegativeCounts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.SaveTokenObservations(context.Background(), []capacity.TokenObservation{{Provider: "claude", Source: "gatepost", ObservedAt: time.Now(), InputTokens: -1}})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestListTokenObservationsBySourceExcludesAmbientUsage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	_, err = db.SaveTokenObservations(context.Background(), []capacity.TokenObservation{
		{Provider: "claude", Source: "redline-run", SourceID: "run:sonnet", ObservedAt: at, Model: "sonnet-5", InputTokens: 10},
		{Provider: "claude", Source: "gatepost", SourceID: "session", ObservedAt: at, Model: "sonnet-5", InputTokens: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.ListTokenObservationsBySource(context.Background(), "claude", "redline-run", at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "run:sonnet" {
		t.Fatalf("observations = %#v", got)
	}
}

// TestLatestTokenObservationTime verifies that LatestTokenObservationTime
// returns a zero time when no rows exist and the exact saved timestamp when
// rows are present.  This function drives ingestion cursors: a wrong return
// value causes the system to either re-ingest all history (zero when rows
// exist) or skip new observations (non-zero when the table is empty).
func TestLatestTokenObservationTime(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// No observations yet — must return zero time, not an error.
	got, err := db.LatestTokenObservationTime(ctx, "claude", "gatepost-pi")
	if err != nil {
		t.Fatalf("empty table: unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("empty table: want zero time, got %v", got)
	}

	// Save two observations with different timestamps; the later one wins.
	earlier := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 7, 17, 12, 30, 0, 123456789, time.UTC)
	items := []capacity.TokenObservation{
		{Provider: "claude", Source: "gatepost-pi", SourceID: "s:early", ObservedAt: earlier, Model: "opus", InputTokens: 1},
		{Provider: "claude", Source: "gatepost-pi", SourceID: "s:late", ObservedAt: later, Model: "opus", InputTokens: 2},
		// A different source that should not affect the result.
		{Provider: "claude", Source: "gatepost", SourceID: "s:other", ObservedAt: later.Add(time.Hour), Model: "opus", InputTokens: 3},
	}
	if _, err := db.SaveTokenObservations(ctx, items); err != nil {
		t.Fatal(err)
	}

	got, err = db.LatestTokenObservationTime(ctx, "claude", "gatepost-pi")
	if err != nil {
		t.Fatalf("after save: unexpected error: %v", err)
	}
	// The returned time must match the later timestamp exactly (nanosecond
	// precision is preserved via RFC3339Nano formatting).
	if !got.Equal(later) {
		t.Fatalf("want %v, got %v", later, got)
	}

	// Querying a different source must still return zero (no rows for that key).
	got, err = db.LatestTokenObservationTime(ctx, "claude", "nonexistent-source")
	if err != nil {
		t.Fatalf("missing source: unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("missing source: want zero time, got %v", got)
	}
}
