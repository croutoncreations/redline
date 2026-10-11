package openusage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/openusage"
)

// A synthetic openusage.limits.v1 family response with two Claude account
// cards, shaped like upstream docs/local-http-api.md. Display names carry an
// email, as upstream builds them; Redline must never store it.
const twoCardsFixture = `{
  "schema": "openusage.limits.v1",
  "generatedAt": "2026-10-10T12:00:00Z",
  "providers": {
    "claude": {
      "displayName": "Claude: Example Org (fixture-one@example.invalid)",
      "plan": "Max 20x",
      "fetchedAt": "2026-10-10T11:59:00Z",
      "expiresAt": "2026-10-10T12:04:00Z",
      "stale": false,
      "resources": {
        "session": {"kind":"consumption","unit":"percent","used":36,"limit":100,"remaining":64,"resetsAt":"2026-10-10T16:55:00Z","windowSeconds":18000},
        "weekly":  {"kind":"consumption","unit":"percent","used":59,"limit":100,"remaining":41,"resetsAt":"2026-10-13T16:00:00Z","windowSeconds":604800},
        "fable":   {"kind":"consumption","unit":"percent","used":28,"limit":100,"remaining":72},
        "rateLimitResets": {"kind":"balance","unit":"count","available":2,"expiresAt":["2026-10-20T00:00:00Z","2026-10-12T00:00:00Z","2026-10-01T00:00:00Z"]},
        "extraUsage": {"kind":"consumption","unit":"usd","used":4.5}
      }
    },
    "claude@ab12cd34": {
      "displayName": "Claude: Client Org (fixture-two@example.invalid)",
      "plan": "Team",
      "fetchedAt": "2026-10-10T11:58:00Z",
      "expiresAt": "2026-10-10T12:03:00Z",
      "stale": true,
      "resources": {
        "session": {"kind":"consumption","unit":"percent","used":100,"limit":100,"remaining":0,"resetsAt":"2026-10-10T15:57:00Z"},
        "weekly":  {"kind":"consumption","unit":"percent","used":42,"limit":100,"remaining":58,"resetsAt":"2026-10-15T03:00:00Z"}
      }
    }
  },
  "errors": [{"providerId":"claude@ab12cd34","message":"Updates blocked by Anthropic for fixture-two@example.invalid"}]
}`

func TestSnapshotFromCardSelectsOnlyTheNamedCard(t *testing.T) {
	envelope, err := openusage.ParseLimits([]byte(twoCardsFixture))
	if err != nil {
		t.Fatal(err)
	}
	personal, err := openusage.SnapshotFromCard(envelope, "claude", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if personal.Plan != "Max 20x" || personal.SourceStale || personal.Short == nil {
		t.Fatalf("personal = %+v", personal)
	}
	assertClose(t, personal.Weekly.Remaining, .41)
	assertClose(t, personal.Short.Remaining, .64)
	fable, ok := personal.Allowance("model:fable:weekly")
	if !ok || !fable.ResetInferred || !fable.ResetsAt.Equal(personal.Weekly.ResetsAt) {
		t.Fatalf("fable = %+v ok=%v (missing reset must be inferred from weekly and marked)", fable, ok)
	}
	// Reset credits keep their own expiries; a lapsed one is dropped, the
	// rest are sorted, and the soonest is the summary expiry.
	if personal.BankedResets == nil || *personal.BankedResets != 2 || len(personal.BankedResetsExpiries) != 2 ||
		personal.BankedResetsExpireAt == nil || !personal.BankedResetsExpireAt.Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("resets = %v %v %v", personal.BankedResets, personal.BankedResetsExpireAt, personal.BankedResetsExpiries)
	}
	// Window resets are separate from reset-credit deadlines.
	if !personal.Weekly.ResetsAt.Equal(time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly reset = %v", personal.Weekly.ResetsAt)
	}

	client, err := openusage.SnapshotFromCard(envelope, "claude", "claude@ab12cd34")
	if err != nil {
		t.Fatal(err)
	}
	if client.Plan != "Team" || !client.SourceStale || client.Confidence != "medium" || client.Short == nil {
		t.Fatalf("client = %+v", client)
	}
	assertClose(t, client.Weekly.Remaining, .58)
	assertClose(t, client.Short.Remaining, 0)
	if client.BankedResets != nil {
		t.Fatalf("a card that does not report resets must leave them unknown, got %v", *client.BankedResets)
	}
}

func TestSnapshotFromCardNeverFallsBackToAnotherCard(t *testing.T) {
	envelope, err := openusage.ParseLimits([]byte(twoCardsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openusage.SnapshotFromCard(envelope, "claude", "claude@ffffffff"); !errors.Is(err, openusage.ErrCardNotFound) {
		t.Fatalf("err = %v, want card not found", err)
	}
}

func TestFetchCardReadsExactRouteAndStoresNoEmail(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(twoCardsFixture))
	}))
	defer server.Close()
	snapshot, raw, err := (openusage.Client{BaseURL: server.URL}).FetchCard(context.Background(), "claude", "claude@ab12cd34")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/limits/claude@ab12cd34" && path != "/v1/limits/claude%40ab12cd34" {
		t.Fatalf("path = %q", path)
	}
	if snapshot.Plan != "Team" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if strings.Contains(string(raw), "@example.invalid") || strings.Contains(string(raw), "displayName") ||
		strings.Contains(string(raw), "fixture-one") {
		t.Fatalf("raw payload kept identity data: %s", raw)
	}
}

func TestSourceUsesCardOnlyWhenConfigured(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/v1/limits/") {
			_, _ = w.Write([]byte(twoCardsFixture))
			return
		}
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	source := openusage.Source{}
	if _, _, err := source.Fetch(context.Background(), config.Provider{Provider: "claude", OpenUsageURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.Fetch(context.Background(), config.Provider{Provider: "claude", OpenUsageURL: server.URL, OpenUsageCard: "claude@ab12cd34"}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/usage/claude" || !strings.HasPrefix(paths[1], "/v1/limits/claude") {
		t.Fatalf("paths = %v (legacy route without a card, limits route with one)", paths)
	}
}

func TestFetchCardMissingRouteIsCardNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"provider_not_found"}`))
	}))
	defer server.Close()
	if _, _, err := (openusage.Client{BaseURL: server.URL}).FetchCard(context.Background(), "claude", "claude@00000000"); !errors.Is(err, openusage.ErrCardNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListCardsOmitsDisplayNamesAndRedactsEmails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(twoCardsFixture))
	}))
	defer server.Close()
	cards, err := (openusage.Client{BaseURL: server.URL}).ListCards(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].ID != "claude" || cards[1].ID != "claude@ab12cd34" || !cards[1].Stale || cards[1].Plan != "Team" {
		t.Fatalf("cards = %+v", cards)
	}
	if strings.Contains(cards[1].Error, "@example.invalid") || !strings.Contains(cards[1].Error, "[account]") {
		t.Fatalf("error not redacted: %q", cards[1].Error)
	}
}

func TestResetCreditsRequireAWholeCount(t *testing.T) {
	body := strings.Replace(twoCardsFixture, `"available":2`, `"available":1.5`, 1)
	envelope, err := openusage.ParseLimits([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := openusage.SnapshotFromCard(envelope, "claude", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets != nil {
		t.Fatal("a fractional reset count is not trusted; it must stay unknown, not be rounded")
	}
}

func TestRedactEmailsKeepsCardIDs(t *testing.T) {
	got := openusage.RedactEmails("card claude@ab12cd34 for a.b+c@example.com and root@localhost (x@y.z)")
	if got != "card claude@ab12cd34 for [account] and [account] ([account])" {
		t.Fatalf("got %q", got)
	}
	// A transport error naming the card URL keeps the port and card; an
	// address on a provider-like name is still an address.
	wrapped := `fetch OpenUsage limits: Get "http://127.0.0.1:6736/v1/limits/claude@ab12cd34": dial tcp 127.0.0.1:6736: connect: connection refused; claude@example.com`
	got = openusage.RedactEmails(wrapped)
	if !strings.Contains(got, "127.0.0.1:6736/v1/limits/claude@ab12cd34") || strings.Contains(got, "example.com") {
		t.Fatalf("got %q", got)
	}
}

// Only OpenUsage's real card-id shape is exempt: a family name, "@", and the
// first 8 lowercase hex digits of a SHA-256 (a salted id on a collision is
// re-hashed, so it has the same shape). A dotless address whose local part
// happens to be a provider family is still an address and must be redacted.
func TestRedactEmailsRedactsDotlessAddressesOnProviderNames(t *testing.T) {
	for _, address := range []string{
		"claude@localhost",
		"codex@intranet",
		"cursor@buildhost",
		"claude@mailhost-2",
		"claude@ab12cd34-2",    // not a card id: card ids carry no suffix
		"claude@ab12cd345",     // 9 hex digits
		"claude@ab12cd3",       // 7 hex digits
		"claude@AB12CD34",      // upstream mints lowercase hex only
		"claude@a_b2cd34",      // not hex
		"claude@d3m0c11e-team", // hex-looking prefix is not enough
	} {
		got := openusage.RedactEmails("upstream rejected " + address + " today")
		if got != "upstream rejected [account] today" {
			t.Errorf("RedactEmails(%q) = %q, want the address redacted", address, got)
		}
	}
	for _, card := range []string{"claude@ab12cd34", "codex@0123abcd", "cursor@ffffffff", "claude@00000000"} {
		if got := openusage.RedactEmails("card " + card + " failed"); got != "card "+card+" failed" {
			t.Errorf("RedactEmails(%q) = %q, want the card id kept", card, got)
		}
	}
}
