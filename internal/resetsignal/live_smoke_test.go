package resetsignal

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

var liveSmokeNow = time.Date(2026, 10, 25, 6, 15, 0, 0, time.UTC)

func liveSmokeSources(t *testing.T) []SourceResult {
	t.Helper()
	var payloads map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, "live-2026-10-25.json"), &payloads); err != nil {
		t.Fatal(err)
	}
	bodies := make(map[string]json.RawMessage)
	for _, source := range DefaultSources() {
		bodies[source.Endpoint] = payloads[source.Name]
	}
	fetcher := Fetcher{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return response(http.StatusOK, "application/json", string(bodies[r.URL.String()]), http.Header{"X-Published-Expires-At": {"2026-10-25T06:30:00Z"}}), nil
		})},
		Now: func() time.Time { return liveSmokeNow },
	}
	results := FetchAll(context.Background(), fetcher, nil)
	for _, result := range results {
		if result.Signal == nil || result.Error != nil {
			t.Fatalf("fixture fetch=%+v", result)
		}
	}
	return results
}

func TestLiveSmokeQuoteProvenance(t *testing.T) {
	results := liveSmokeSources(t)
	for i, want := range []string{"original_post", "original_post", "tracker_summary"} {
		data, err := json.Marshal(results[i].Signal)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["quote_source"] != want {
			t.Errorf("%s quote_source=%v want %s", results[i].Signal.Feed, fields["quote_source"], want)
		}
		if fields["attribution_url"] != DefaultSources()[i].AttributionURL {
			t.Errorf("%s attribution_url=%v", results[i].Signal.Feed, fields["attribution_url"])
		}
	}
	if got := results[1].Signal.Quote; strings.Contains(got, "t.co") {
		t.Errorf("original quote retains trailing link: %q", got)
	}
	a := ViableAnnouncement(BuildReport(liveSmokeNow, results[2:]))
	if a == nil {
		t.Fatal("no summary announcement")
	}
	e := a.Notification(liveSmokeNow, false)
	want := "Codex global reset reported complete (codex-reset: \"Reset observed by operator; awaiting Tibo's confirmation.\"). Source post by @thsottiaux: https://x.com/thsottiaux/status/2114200291319124163. Seen by codex-reset. Data: https://codex-reset.com/."
	if e.Message != want || e.Data["quote_source"] != "tracker_summary" || e.Data["attribution"] != "Data: codex-reset.com" || e.Data["attribution_urls"] != "https://codex-reset.com/" {
		t.Errorf("summary notification=%+v", e)
	}
	t.Log(e.Message)
}

func TestLiveSmokeBestBackedAnnouncement(t *testing.T) {
	results := liveSmokeSources(t)
	r := BuildReport(liveSmokeNow, results)
	a := ViableAnnouncement(r)
	const url = "https://x.com/thsottiaux/status/2114237083652882932"
	const quote = "It is all propagated. Enjoy some mad codexing tomorrow. Fun week ahead too."
	if a == nil || a.URL != url || a.Key != url || a.Kind != KindCompleted || a.Quote != quote || a.Account != "thsottiaux" || strings.Join(a.Feeds, ",") != "resetbeacon,willreset" {
		t.Fatalf("announcement=%+v", a)
	}
	if r.State != KindCompleted || !strings.Contains(r.Summary, "completed global event") {
		t.Fatalf("report changed: %+v", r)
	}
	var selected []Signal
	summarizeCurrentSelection(liveSmokeNow, results, &selected)
	if len(selected) != 1 || selected[0].Feed != "codex-reset" {
		t.Fatalf("report selection changed: %+v", selected)
	}
	want := "Tibo (@thsottiaux): \"" + quote + "\" Global Codex reset reported complete. Seen by resetbeacon, willreset. Data: resetbeacon.com, willreset.com."
	if e := a.Notification(liveSmokeNow, false); e.Message != want || e.Data["quote_source"] != "original_post" || e.Data["attribution"] != "Data: resetbeacon.com,Data: willreset.com" || e.Data["attribution_urls"] != "https://resetbeacon.com/,https://willreset.com/" {
		t.Fatalf("notification=%+v", e)
	}
	t.Log(want)
	// API and CLI both serialize this cached State shape without another fetch.
	data, err := json.Marshal(State{Enabled: true, Report: &r, Announcement: a})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"attribution_urls":"https://resetbeacon.com/,https://willreset.com/"`) || !strings.Contains(string(data), `"attribution_url":"https://codex-reset.com/"`) {
		t.Fatalf("cached state lacks linked credits: %s", data)
	}
}

func TestLiveSmokeAnnouncementRanking(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func([]SourceResult) []SourceResult
		wantFeed string
		wantKind Kind
	}{
		{"original quote before kind", func(r []SourceResult) []SourceResult {
			r[0].Signal.Kind = KindPromised
			return []SourceResult{r[0], r[2]}
		}, "resetbeacon", KindPromised},
		{"kind before time", func(r []SourceResult) []SourceResult {
			r[0].Signal.QuoteSource = "tracker_summary"
			r[0].Signal.Kind = KindPromised
			return []SourceResult{r[0], r[2]}
		}, "codex-reset", KindCompleted},
		{"time before key", func(r []SourceResult) []SourceResult {
			r[0].Signal.QuoteSource = "tracker_summary"
			return []SourceResult{r[0], r[2]}
		}, "codex-reset", KindCompleted},
		{"key breaks tie", func(r []SourceResult) []SourceResult {
			r[0].Signal.QuoteSource = "tracker_summary"
			r[0].Signal.PublishedAt = r[2].Signal.PublishedAt
			return []SourceResult{r[0], r[2]}
		}, "codex-reset", KindCompleted},
		{"duplicate feed is not another mirror", func(r []SourceResult) []SourceResult {
			r[0].Signal.QuoteSource = "tracker_summary"
			return []SourceResult{r[0], r[0], r[2]}
		}, "codex-reset", KindCompleted},
		{"quote member need not decide kind", func(r []SourceResult) []SourceResult {
			r[0].Signal.Kind = KindPromised
			r[1].Signal.Kind = KindCompleted
			r[1].Signal.QuoteSource = "tracker_summary"
			r[1].Signal.Freshness = Fresh
			r[1].Signal.PublishedAt = r[2].Signal.PublishedAt
			return r
		}, "resetbeacon", KindCompleted},
		{"stale mirror cannot decide kind", func(r []SourceResult) []SourceResult {
			r[0].Signal.Kind = KindPromised
			r[1].Signal.Kind = KindCompleted
			r[1].Signal.Freshness = Stale
			r[1].Signal.PublishedAt = r[2].Signal.PublishedAt
			return r
		}, "resetbeacon", KindPromised},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.change(liveSmokeSources(t))
			a := ViableAnnouncement(BuildReport(liveSmokeNow, r))
			if a == nil || a.QuoteFeed != tc.wantFeed || a.Kind != tc.wantKind {
				t.Fatalf("announcement=%+v", a)
			}
		})
	}
	t.Run("selected retraction still blocks fallback", func(t *testing.T) {
		r := liveSmokeSources(t)
		r[2].Signal.Withdrawn = true
		if got := ViableAnnouncement(BuildReport(liveSmokeNow, r)); got != nil {
			t.Fatalf("announcement=%+v", got)
		}
	})
	t.Run("newest original member supplies quote and account", func(t *testing.T) {
		r := liveSmokeSources(t)
		r[1].Signal.Freshness = Fresh
		r[1].Signal.PublishedAt = r[2].Signal.PublishedAt
		r[1].Signal.OriginalAccount = "mirror-account"
		r[1].Signal.Quote = "Newer original text."
		a := ViableAnnouncement(BuildReport(liveSmokeNow, r))
		if a == nil || a.Quote != "Newer original text." || a.Account != "mirror-account" || a.Kind != KindCompleted {
			t.Fatalf("announcement=%+v", a)
		}
	})
}

func TestFixtureSummaryFieldsAreNotOriginalQuotes(t *testing.T) {
	s, err := normalizeCodexReset(fixture(t, "codex-reset-null-window.json"), nil, fixtureNow)
	if err != nil || s.QuoteSource != "tracker_summary" {
		t.Fatalf("official signal=%+v error=%v", s, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(fixture(t, "willreset-promised.json"), &payload); err != nil {
		t.Fatal(err)
	}
	payload["promise_quote"] = ""
	payload["last_reset_text"] = "Tracker description"
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	s, err = normalizeWillReset(body, nil, fixtureNow)
	if err != nil || s.QuoteSource != "tracker_summary" {
		t.Fatalf("willreset fallback=%+v error=%v", s, err)
	}
}

func TestLiveSmokePromisedAndRetractedMessages(t *testing.T) {
	results := liveSmokeSources(t)
	a := ViableAnnouncement(BuildReport(liveSmokeNow, results))
	if a == nil {
		t.Fatal("no announcement")
	}
	var payload willResetPayload
	if err := json.Unmarshal(fixture(t, "willreset-promised.json"), &payload); err != nil {
		t.Fatal(err)
	}
	a.Kind, a.Quote = KindPromised, payload.PromiseQuote
	end := liveSmokeNow.Add(2 * time.Hour)
	a.WindowEnd, a.WindowCertainty = &end, WindowMirrorInterpretation
	want := "Tibo (@thsottiaux): \"Global reset landing by EOD PST time. Enjoy\". Global Codex reset promised by Sun 08:15 UTC (tracker's reading). Seen by resetbeacon, willreset. Data: resetbeacon.com, willreset.com."
	if got := a.Notification(liveSmokeNow, false).Message; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Log(want)
	want = "Retracted: the earlier Codex reset announcement from @thsottiaux was withdrawn or corrected. Data: resetbeacon.com, willreset.com."
	if got := a.Notification(liveSmokeNow, true).Message; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	t.Log(want)
}

func TestOriginalQuoteTrailingLinks(t *testing.T) {
	base := liveSmokeSources(t)[1].Signal.Quote
	for _, tc := range []struct{ suffix, want string }{
		{" https://t.co/first https://t.co/second", base},
		{" https://example.com/post", base + " https://example.com/post"},
		{" https://t.co/embedded more text", base + " https://t.co/embedded more text"},
		{" https://t.co.evil.example/post", base + " https://t.co.evil.example/post"},
	} {
		if got := stripTrailingPostLinks(base + tc.suffix); got != tc.want {
			t.Errorf("suffix=%q got=%q want=%q", tc.suffix, got, tc.want)
		}
	}
}

func TestAnnouncementQuotePunctuation(t *testing.T) {
	var cases []struct{ Quote, Sentence string }
	if err := json.Unmarshal(fixture(t, "announcement-punctuation.json"), &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Quote, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"kind": "COMPLETED", "account": "thsottiaux", "quote": tc.Quote, "quote_source": "original_post", "feeds": []string{"resetbeacon"}})
			var a Announcement
			if err := json.Unmarshal(raw, &a); err != nil {
				t.Fatal(err)
			}
			want := tc.Sentence + " Global Codex reset reported complete. Seen by resetbeacon."
			if got := a.Notification(liveSmokeNow, false).Message; got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
}
