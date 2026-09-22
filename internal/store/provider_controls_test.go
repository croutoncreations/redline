package store_test

import (
	"context"
	"testing"
)

func TestProviderPauseStatePersists(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	paused, err := db.ProviderPaused(ctx, "codex-main")
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := db.SetProviderPaused(ctx, "codex-main", false); err != nil {
		t.Fatal(err)
	}
	paused, err = db.ProviderPaused(ctx, "codex-main")
	if err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
}

func TestProviderPolicyOverridePersistsWithoutChangingPause(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", "early"); err != nil {
		t.Fatal(err)
	}
	policy, err := db.ProviderPolicy(ctx, "codex-main")
	if err != nil || policy != "early" {
		t.Fatalf("policy=%q err=%v", policy, err)
	}
	paused, err := db.ProviderPaused(ctx, "codex-main")
	if err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", ""); err != nil {
		t.Fatal(err)
	}
	policy, err = db.ProviderPolicy(ctx, "codex-main")
	if err != nil || policy != "" {
		t.Fatalf("cleared policy=%q err=%v", policy, err)
	}
}

func TestProviderConcurrencyOverridePersistsWithoutChangingOtherControls(t *testing.T) {
	db := openTaskDB(t)
	ctx := context.Background()
	if err := db.SetProviderPaused(ctx, "codex-main", true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderPolicy(ctx, "codex-main", "early"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProviderMaxConcurrentRuns(ctx, "codex-main", 3); err != nil {
		t.Fatal(err)
	}
	limit, err := db.ProviderMaxConcurrentRuns(ctx, "codex-main")
	if err != nil || limit != 3 {
		t.Fatalf("limit=%d err=%v", limit, err)
	}
	paused, _ := db.ProviderPaused(ctx, "codex-main")
	policy, _ := db.ProviderPolicy(ctx, "codex-main")
	if !paused || policy != "early" {
		t.Fatalf("paused=%t policy=%q", paused, policy)
	}
	if err := db.SetProviderMaxConcurrentRuns(ctx, "codex-main", 0); err != nil {
		t.Fatal(err)
	}
	limit, err = db.ProviderMaxConcurrentRuns(ctx, "codex-main")
	if err != nil || limit != 0 {
		t.Fatalf("cleared limit=%d err=%v", limit, err)
	}
}
