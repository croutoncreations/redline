package core_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/croutoncreations/redline/mobile/core"
)

func usageWithDetails(t *testing.T, banked int, details string) *core.ResetCredits {
	t.Helper()
	server := usagePayload(t, `{"generated_at":"2026-07-20T19:00:00Z","providers":[
		{"id":"claude-main","provider":"claude","account_status":"ok","snapshot":{"provider":"claude","observed_at":"2026-07-20T18:59:00Z","source":"native",
		 "weekly":{"remaining":0.5,"resets_at":"2026-07-24T00:00:00Z"},"banked_resets":`+itoa(banked)+`,
		 "banked_reset_details":`+details+`}}]}`)
	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	return decodeUsage(t, raw).Providers[0].ResetCredits
}

func itoa(n int) string { return string(rune('0' + n)) }

// One full reset and three Fable resets, as Claude reports them: the phone
// gets them by kind, full first, each with its own deadlines.
func TestResetCreditsAreGroupedByKind(t *testing.T) {
	credits := usageWithDetails(t, 4, `[
		{"id":"f1","kind":"fable","expires_at":"2026-09-09T07:59:00Z","resets_left":1},
		{"id":"full","kind":"full","expires_at":"2026-08-16T04:59:00Z","resets_left":1},
		{"id":"f2","kind":"fable","expires_at":"2026-09-09T07:59:00Z","resets_left":2}]`)
	if credits == nil || credits.Available != 4 || len(credits.Kinds) != 2 {
		t.Fatalf("credits = %+v", credits)
	}
	full, fable := credits.Kinds[0], credits.Kinds[1]
	if full.Kind != "full" || full.Label != "Full" || full.Available != 1 || len(full.Expiries) != 1 {
		t.Fatalf("full = %+v", full)
	}
	if fable.Kind != "fable" || fable.Label != "Fable" || fable.Available != 3 || len(fable.Expiries) != 3 {
		t.Fatalf("fable = %+v", fable)
	}
}

// Paused and not-yet-active resets are owned but not in the count: listed as
// held, with Claude Code's reason, and the breakdown still matches the count.
func TestHeldResetsAreListedWithTheirReason(t *testing.T) {
	credits := usageWithDetails(t, 1, `[
		{"id":"full","kind":"full","expires_at":"2026-08-16T04:59:00Z","resets_left":1},
		{"id":"p","kind":"fable","paused":true,"unavailable_reason":"paused","resets_left":2},
		{"id":"s","kind":"fable","starts_at":"2026-07-25T00:00:00Z","unavailable_reason":"not_started","resets_left":1}]`)
	if len(credits.Kinds) != 2 {
		t.Fatalf("kinds = %+v", credits.Kinds)
	}
	fable := credits.Kinds[1]
	if fable.Available != 0 || fable.Held != 3 || !reflect.DeepEqual(fable.Notes, []string{"2 paused", "1 isn't active yet"}) {
		t.Fatalf("fable = %+v", fable)
	}
}

// A breakdown that does not add up to the headline count (stale or partial
// details) is not sent; the phone falls back to the plain count.
func TestMismatchedDetailsAreNotBrokenDown(t *testing.T) {
	credits := usageWithDetails(t, 4, `[{"id":"full","kind":"full","resets_left":1}]`)
	if credits == nil || credits.Available != 4 || credits.Kinds != nil {
		t.Fatalf("credits = %+v", credits)
	}
	if none := usageWithDetails(t, 2, `[]`); none.Kinds != nil {
		t.Fatalf("kinds without details = %+v", none.Kinds)
	}
}

// Expired details are dropped, and an unknown kind beside known ones is
// grouped as Other rather than hidden.
func TestExpiredAndUnknownKinds(t *testing.T) {
	credits := usageWithDetails(t, 2, `[
		{"id":"old","kind":"full","expires_at":"2026-07-01T00:00:00Z","resets_left":1},
		{"id":"f","kind":"fable","resets_left":1},
		{"id":"x","kind":"mystery","resets_left":1}]`)
	if len(credits.Kinds) != 2 || credits.Kinds[0].Label != "Fable" || credits.Kinds[1].Label != "Other" || credits.Kinds[1].Available != 1 {
		t.Fatalf("kinds = %+v", credits.Kinds)
	}
}

// Credits that never say what they refill (Codex) are not broken down at
// all: "Other 2" would replace the count and deadlines with less.
func TestResetsWithNoKindAreNotBrokenDown(t *testing.T) {
	credits := usageWithDetails(t, 2, `[
		{"id":"c1","expires_at":"2026-07-30T00:00:00Z","resets_left":1},
		{"id":"c2","expires_at":"2026-08-02T00:00:00Z","resets_left":1}]`)
	if credits == nil || credits.Available != 2 || credits.Kinds != nil {
		t.Fatalf("credits = %+v", credits)
	}
}
