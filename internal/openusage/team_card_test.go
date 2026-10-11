package openusage

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// limits-claude-team-no-weekly.json is a synthetic OpenUsage answer in the
// shape OpenUsage v0.7.14 returns for Team seats: placeholder card ids, no
// display names or emails, made-up numbers and timestamps. The default
// card is a personal Max plan with a weekly window; the five Team seats
// report the 5-hour window, Fable's weekly window and a reset balance, and
// no account-wide weekly window.
func ownerEnvelope(t *testing.T) LimitsEnvelope {
	t.Helper()
	data, err := os.ReadFile("testdata/limits-claude-team-no-weekly.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope LimitsEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestTeamCardsWithoutAWeeklyWindowKeepWhatTheyReport(t *testing.T) {
	envelope := ownerEnvelope(t)
	for _, card := range []string{"claude@a0000002", "claude@a0000003", "claude@a0000004", "claude@a0000005", "claude@a0000006"} {
		snapshot, err := SnapshotFromCard(envelope, "claude", card)
		if err != nil {
			t.Fatalf("%s: %v", card, err)
		}
		if !snapshot.WeeklyUnavailable || snapshot.Weekly.Remaining != 0 || !snapshot.Weekly.ResetsAt.IsZero() {
			t.Fatalf("%s: weekly must be 'not reported', got %#v unavailable=%v", card, snapshot.Weekly, snapshot.WeeklyUnavailable)
		}
		if snapshot.Plan != "Team 5x" || snapshot.SourceStale {
			t.Fatalf("%s: plan=%q stale=%v", card, snapshot.Plan, snapshot.SourceStale)
		}
		// The model window is kept as a model window with its own reset,
		// never promoted to the account's weekly limit.
		fable, ok := snapshot.Allowance("model:fable:weekly")
		if !ok || fable.Scope != "model" || fable.ResetsAt.IsZero() || fable.ResetInferred {
			t.Fatalf("%s: fable = %#v ok=%v", card, fable, ok)
		}
		if _, ok := snapshot.Allowance("weekly"); ok {
			t.Fatalf("%s: a weekly allowance was invented", card)
		}
		// Reset credits: a reported zero is zero, not unknown.
		if snapshot.BankedResets == nil || *snapshot.BankedResets != 0 || snapshot.BankedResetsExpireAt != nil {
			t.Fatalf("%s: banked = %v expire=%v", card, snapshot.BankedResets, snapshot.BankedResetsExpireAt)
		}
		if err := snapshot.Validate(); err != nil {
			t.Fatalf("%s: %v", card, err)
		}
	}
}

func TestTeamCardFiveHourWindowIsKeptAsReported(t *testing.T) {
	envelope := ownerEnvelope(t)
	// One seat has used its 5-hour window (30% used, reset reported); the
	// rest have not started it.
	running, err := SnapshotFromCard(envelope, "claude", "claude@a0000004")
	if err != nil {
		t.Fatal(err)
	}
	if running.Short == nil || math.Abs(running.Short.Remaining-0.70) > 1e-9 || running.Short.ResetsAt.IsZero() || running.ShortWindowNotStarted {
		t.Fatalf("short = %#v notStarted=%v", running.Short, running.ShortWindowNotStarted)
	}
	idle, err := SnapshotFromCard(envelope, "claude", "claude@a0000002")
	if err != nil {
		t.Fatal(err)
	}
	if idle.Short != nil || !idle.ShortWindowNotStarted || idle.ShortWindowUnavailable {
		t.Fatalf("idle short = %#v notStarted=%v unavailable=%v", idle.Short, idle.ShortWindowNotStarted, idle.ShortWindowUnavailable)
	}
	// The 5-hour figure is never copied into the weekly window.
	if running.Weekly.Remaining == running.Short.Remaining {
		t.Fatal("the 5-hour percentage leaked into the weekly window")
	}
}

func TestPersonalCardWithAWeeklyWindowIsUnchanged(t *testing.T) {
	snapshot, err := SnapshotFromCard(ownerEnvelope(t), "claude", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WeeklyUnavailable || snapshot.Weekly.ResetsAt.IsZero() || math.Abs(snapshot.Weekly.Remaining-0.40) > 1e-9 {
		t.Fatalf("weekly = %#v unavailable=%v", snapshot.Weekly, snapshot.WeeklyUnavailable)
	}
	if snapshot.BankedResets == nil || *snapshot.BankedResets != 1 || len(snapshot.BankedResetsExpiries) != 1 {
		t.Fatalf("credits = %v %v", snapshot.BankedResets, snapshot.BankedResetsExpiries)
	}
}

// A weekly window OpenUsage lists but cannot fill, or a card error, is still
// a failure to read -- not an account without a weekly limit.
func TestAListedButUnreadableWeeklyWindowIsStillAnError(t *testing.T) {
	envelope := ownerEnvelope(t)
	card := envelope.Providers["claude@a0000002"]
	resources := map[string]LimitsResource{}
	for key, value := range card.Resources {
		resources[key] = value
	}
	resources["weekly"] = LimitsResource{Kind: "consumption", Unit: "percent"}
	card.Resources = resources
	envelope.Providers["claude@a0000002"] = card
	if _, err := SnapshotFromCard(envelope, "claude", "claude@a0000002"); err == nil || !strings.Contains(err.Error(), "no weekly limit") {
		t.Fatalf("err = %v", err)
	}
	failing := ownerEnvelope(t)
	failing.Errors = append(failing.Errors, LimitsError{ProviderID: "claude@a0000003", Message: "upstream failed for someone@example.invalid"})
	_, err := SnapshotFromCard(failing, "claude", "claude@a0000003")
	if err == nil || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("err = %v", err)
	}
	empty := ownerEnvelope(t)
	bare := empty.Providers["claude@a0000002"]
	bare.Resources = map[string]LimitsResource{}
	empty.Providers["claude@a0000002"] = bare
	if _, err := SnapshotFromCard(empty, "claude", "claude@a0000002"); err == nil {
		t.Fatal("a card reporting nothing at all must be an error")
	}
	_ = time.Now
}
