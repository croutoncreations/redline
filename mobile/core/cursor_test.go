package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var cursorNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// cursorCard is the desktop's cursor_usage member as the recorded OpenUsage
// card renders it: Cursor Models 0%, Other Models used up, Grok Bot 58%,
// on-demand spend with no limit, and credits.
func cursorCard(expiresIn time.Duration) string {
	fetched := cursorNow.Add(-time.Minute).Format(time.RFC3339)
	expires := cursorNow.Add(expiresIn).Format(time.RFC3339)
	month := cursorNow.Add(4 * 24 * time.Hour).Format(time.RFC3339)
	week := cursorNow.Add(2 * 24 * time.Hour).Format(time.RFC3339)
	return fmt.Sprintf(`{"id":"cursor","label":"Cursor","plan":"Pro","source":"openusage",
	  "fetched_at":%q,"expires_at":%q,"stale":false,"meters":[
	  {"key":"autoUsage","label":"Cursor Models","group":"monthly","unit":"percent","used":0,"limit":100,"resets_at":%q},
	  {"key":"apiUsage","label":"Other Models","group":"monthly","unit":"percent","used":100,"limit":100,"resets_at":%q},
	  {"key":"grokBot","label":"Grok Bot","group":"weekly","unit":"percent","used":58,"limit":100,"resets_at":%q},
	  {"key":"onDemand","label":"On-demand","group":"on_demand","unit":"usd","used":1.2}],
	  "balances":[{"key":"credits","label":"Credits","unit":"usd","available":22.23}]}`,
		fetched, expires, month, month, week)
}

func renderPayload(t *testing.T, body string) UsageView {
	t.Helper()
	var payload dashboardPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	return renderUsage(payload, cursorNow)
}

func TestCursorCardKeepsPoolsGrokBotOnDemandAndCreditsApart(t *testing.T) {
	view := renderPayload(t, `{"providers":[],"cursor_usage":`+cursorCard(4*time.Minute)+`}`)
	card := view.Cursor
	if card == nil || card.State != "Up to date" || card.Stale || card.Scheduled || card.Plan != "Pro" {
		t.Fatalf("card = %#v", card)
	}
	if !strings.Contains(card.Note, "not used for scheduling") {
		t.Fatalf("usage-only card must say so: %q", card.Note)
	}
	var headings []string
	for _, group := range card.Groups {
		headings = append(headings, group.Heading)
	}
	if strings.Join(headings, ",") != "Monthly,Weekly,On-demand" {
		t.Fatalf("groups = %v", headings)
	}
	other := card.Groups[0].Meters[1]
	if other.Label != "Other Models" || !other.Exhausted || other.ValueLabel != "Used up" || other.RemainingPercent != 0 ||
		other.ResetsInSeconds != 4*24*3600 {
		t.Fatalf("other models = %#v", other)
	}
	// What is left, like the provider meters: 58% used is 42% left.
	if grok := card.Groups[1].Meters[0]; grok.ValueLabel != "42% left" || grok.RemainingPercent != 42 {
		t.Fatalf("grok = %#v", grok)
	}
	// No limit: the spend only, never a bar or a percentage.
	if spend := card.Groups[2].Meters[0]; spend.HasLimit || spend.ValueLabel != "$1.20 spent" || spend.ResetsAt != "" {
		t.Fatalf("on-demand = %#v", spend)
	}
	if len(card.Balances) != 1 || card.Balances[0].ValueLabel != "$22.23 available" {
		t.Fatalf("balances = %#v", card.Balances)
	}
}

// Out of date only once the source has missed its refresh: within the
// desktop's two minute grace after expiry, the card is still current.
func TestCursorCardStalenessUsesTheDesktopGrace(t *testing.T) {
	if card := renderPayload(t, `{"cursor_usage":`+cursorCard(-time.Minute)+`}`).Cursor; card.Stale {
		t.Fatalf("within grace reported stale: %#v", card)
	}
	card := renderPayload(t, `{"cursor_usage":`+cursorCard(-3*time.Minute)+`}`).Cursor
	if !card.Stale || card.State != "Out of date" {
		t.Fatalf("past grace = %#v", card)
	}
}

func TestNoCursorUsageMeansNoCard(t *testing.T) {
	if card := renderPayload(t, `{"providers":[]}`).Cursor; card != nil {
		t.Fatalf("card = %#v", card)
	}
}

// With a Cursor provider account the monthly pools gate Cursor jobs, so the
// card must not claim it is unused for scheduling, and the account window is
// named and paced as the month it is.
func TestCursorProviderIsMonthlyAndTheCardSaysWhatSchedules(t *testing.T) {
	reset := cursorNow.Add(15 * 24 * time.Hour).Format(time.RFC3339)
	view := renderPayload(t, `{"providers":[{"id":"cursor-main","provider":"cursor",
	  "snapshot":{"provider":"cursor","observed_at":"2026-10-09T11:59:00Z","source":"openusage",
	    "weekly":{"remaining":0.6,"resets_at":"`+reset+`","period_seconds":2592000}}}],
	  "cursor_usage":`+cursorCard(4*time.Minute)+`}`)
	provider := view.Providers[0]
	if provider.WeeklyLabel != "Monthly allowance" {
		t.Fatalf("label = %q", provider.WeeklyLabel)
	}
	// Half the month gone: the pace mark sits at 50%, not past the end of a week.
	if provider.Weekly.ElapsedPercent != 50 {
		t.Fatalf("elapsed = %d", provider.Weekly.ElapsedPercent)
	}
	if !view.Cursor.Scheduled || strings.Contains(view.Cursor.Note, "not used for scheduling") {
		t.Fatalf("card = %#v", view.Cursor)
	}
}

func TestCursorAccountMismatchIsNamed(t *testing.T) {
	if got := staleReason("cursor", "account_mismatch"); !strings.Contains(got, "different accounts") {
		t.Fatalf("reason = %q", got)
	}
}

func TestCursorSignedOutReasonNamesTheCLILogin(t *testing.T) {
	if got := staleReason("cursor", "signed_out"); !strings.Contains(got, "agent login") {
		t.Fatalf("reason = %q", got)
	}
}

// A desktop from before Cursor usage rejects the unknown member. The screen
// asks again for what it knows rather than failing.
func TestFetchUsageFallsBackForAnOlderDesktop(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Query().Get("fields"), "cursor_usage") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"unknown dashboard field \"cursor_usage\""}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"providers":[{"id":"claude-main","provider":"claude"}]}`)
	}))
	defer server.Close()
	raw, err := NewClientWithClock(server.URL, "t", func() time.Time { return cursorNow }).FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	var view UsageView
	_ = json.Unmarshal([]byte(raw), &view)
	if len(view.Providers) != 1 || view.Cursor != nil || calls.Load() != 2 {
		t.Fatalf("view = %#v calls = %d", view, calls.Load())
	}
}

func TestStreamFallsBackForAnOlderDesktop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("fields"), "cursor_usage") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"unknown dashboard field \"cursor_usage\""}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", usageFrame(cursorNow, 0.7))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	sink := &recorder{}
	stream := NewClientWithClock(server.URL, "t", func() time.Time { return cursorNow }).StreamUsageWithBackoff(sink, 10)
	defer stream.Stop()
	if payloads := sink.waitForPayloads(t, 1); !strings.Contains(payloads[0], "claude-main") {
		t.Fatalf("payload = %s", payloads[0])
	}
}
