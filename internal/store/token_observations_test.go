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
