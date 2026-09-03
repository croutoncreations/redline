package openusage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jfox/redline/internal/openusage"
)

const fixture = `[
  {
    "providerId": "codex",
    "displayName": "Codex",
    "plan": "Plus",
    "lines": [
      {"type":"progress","label":"Session","used":70,"limit":100,"resetsAt":"2026-07-16T22:00:00Z"},
      {"type":"progress","label":"Weekly","used":53,"limit":100,"resetsAt":"2026-07-17T05:00:00Z"}
    ],
    "fetchedAt": "2026-07-16T18:00:00Z"
  },
  {
    "providerId": "claude",
    "displayName": "Claude",
    "lines": [
      {"type":"progress","label":"5-hour","used":20,"limit":100,"resetsAt":"2026-07-16T23:00:00Z"},
      {"type":"progress","label":"7-day","used":10,"limit":100,"resetsAt":"2026-07-20T00:00:00Z"}
    ],
    "fetchedAt": "2026-07-16T18:01:00Z"
  }
]`

func TestParseNormalizesCodexSessionAndWeeklyLines(t *testing.T) {
	got, err := openusage.Parse([]byte(fixture), "codex")
	if err != nil {
		t.Fatal(err)
	}

	if got.Provider != "codex" || got.Source != "openusage" {
		t.Fatalf("provider/source = %q/%q", got.Provider, got.Source)
	}
	assertClose(t, got.Short.Remaining, 0.30)
	assertClose(t, got.Weekly.Remaining, 0.47)
	if got.Short.ResetsAt.Format(time.RFC3339) != "2026-07-16T22:00:00Z" {
		t.Fatalf("short reset = %s", got.Short.ResetsAt)
	}
}

func TestParseAcceptsClaudeWindowLabels(t *testing.T) {
	got, err := openusage.Parse([]byte(fixture), "claude")
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, got.Short.Remaining, 0.80)
	assertClose(t, got.Weekly.Remaining, 0.90)
}

func TestParseAcceptsSingleProviderObject(t *testing.T) {
	payload := `{
      "providerId":"claude",
      "fetchedAt":"2026-07-16T18:00:00Z",
      "lines":[
        {"type":"progress","label":"Session","used":20,"limit":100,"resetsAt":"2026-07-16T23:00:00Z"},
        {"type":"progress","label":"Weekly","used":10,"limit":100,"resetsAt":"2026-07-20T00:00:00Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "claude")
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, got.Short.Remaining, 0.80)
	assertClose(t, got.Weekly.Remaining, 0.90)
}

func TestParseOmitsOptionalShortWindowWithoutReset(t *testing.T) {
	payload := `{
      "providerId":"claude",
      "fetchedAt":"2026-07-25T23:00:00Z",
      "lines":[
        {"type":"progress","label":"Session","used":0,"limit":100,"periodDurationMs":18000000,"resetsAt":""},
        {"type":"progress","label":"Weekly","used":4,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-31T17:00:00Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Short != nil {
		t.Fatalf("short window = %#v, want omitted", got.Short)
	}
	if _, ok := got.Allowance("session"); ok {
		t.Fatalf("session allowance should be omitted: %#v", got.Allowances)
	}
	assertClose(t, got.Weekly.Remaining, .96)
	if got.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium", got.Confidence)
	}
	// Dropping the window silently leaves every client unable to tell "this
	// provider has no five hour limit" from "it has one and we could not read
	// it", and they render those two very differently. Confidence cannot carry
	// this: it also drops to medium for an inferred model weekly reset.
	if !got.ShortWindowUnavailable {
		t.Fatal("a dropped short window must be reported as unavailable")
	}
}

// A provider that simply has no five hour window must not be marked
// unavailable, or every client grows a permanent "unknown" row for it.
func TestParseDoesNotMarkAbsentShortWindowUnavailable(t *testing.T) {
	payload := `{
      "providerId":"codex",
      "fetchedAt":"2026-07-25T23:00:00Z",
      "lines":[
        {"type":"progress","label":"Weekly","used":4,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-31T17:00:00Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ShortWindowUnavailable {
		t.Fatal("a provider with no short window must not be marked unavailable")
	}
}

// An inferred model weekly reset also lowers confidence to medium. That must
// not be mistaken for a missing five hour window.
func TestParseKeepsInferredModelResetSeparateFromAMissingShortWindow(t *testing.T) {
	payload := `{
      "providerId":"claude",
      "fetchedAt":"2026-07-25T23:00:00Z",
      "lines":[
        {"type":"progress","label":"5-hour","used":20,"limit":100,"periodDurationMs":18000000,"resetsAt":"2026-07-26T04:00:00Z"},
        {"type":"progress","label":"Weekly","used":4,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-31T17:00:00Z"},
        {"type":"progress","label":"Fable","used":10,"limit":100,"periodDurationMs":604800000,"resetsAt":""}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium", got.Confidence)
	}
	if got.ShortWindowUnavailable {
		t.Fatal("an inferred model reset must not mark the short window unavailable")
	}
	if got.Short == nil {
		t.Fatal("the short window was present and must be kept")
	}
}

// Codex names its five hour window "Spark", not "Session". The label was not
// in the mapping, so it fell through to the default and was silently dropped:
// the phone showed Codex with a weekly allowance and no short window, which
// reads as "this provider has no five hour limit" when it plainly does.
//
// The period is the giveaway -- 18000000ms is five hours -- and it is the same
// window Claude calls Session, so it maps to the same key.
func TestParseAcceptsCodexSparkAsTheShortWindow(t *testing.T) {
	payload := `{
      "providerId":"codex",
      "plan":"Pro 5x",
      "fetchedAt":"2026-09-03T18:00:00Z",
      "lines":[
        {"type":"progress","label":"Weekly","used":100,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-09-07T03:03:02.000Z"},
        {"type":"progress","label":"Spark","used":20,"limit":100,"periodDurationMs":18000000,"resetsAt":"2026-09-04T00:46:33.000Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Short == nil {
		t.Fatal("Spark is a five hour window and must populate the short window")
	}
	assertClose(t, got.Short.Remaining, .8)
	allowance, ok := got.Allowance("session")
	if !ok {
		t.Fatal("Spark must also appear as the session allowance")
	}
	assertClose(t, allowance.Remaining, .8)
	// Nothing here was guessed, so confidence must not drop.
	if got.Confidence == "medium" {
		t.Fatal("a fully reported snapshot should not be marked medium confidence")
	}
}

// "Spark Weekly" is a different window that happens to share a prefix. Reading
// it as the short one would report a seven day figure as a five hour figure,
// which is worse than dropping it.
func TestParseDoesNotConfuseSparkWeeklyWithSpark(t *testing.T) {
	payload := `{
      "providerId":"codex",
      "fetchedAt":"2026-09-03T18:00:00Z",
      "lines":[
        {"type":"progress","label":"Weekly","used":100,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-09-07T03:03:02.000Z"},
        {"type":"progress","label":"Spark","used":20,"limit":100,"periodDurationMs":18000000,"resetsAt":"2026-09-04T00:46:33.000Z"},
        {"type":"progress","label":"Spark Weekly","used":40,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-09-10T19:46:33.000Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Short == nil {
		t.Fatal("the short window should still come from Spark")
	}
	// Spark is 80% remaining; Spark Weekly is 60%. Getting 0.6 here would mean
	// the weekly line overwrote the short one.
	assertClose(t, got.Short.Remaining, .8)
	// And the account weekly must remain the real Weekly line, not Spark Weekly.
	assertClose(t, got.Weekly.Remaining, 0)
}

func TestParsePreservesClaudeFableAllowance(t *testing.T) {
	payload := `{
      "providerId":"claude",
      "fetchedAt":"2026-07-19T03:27:53.131Z",
      "lines":[
        {"type":"progress","label":"Session","used":0,"limit":100,"periodDurationMs":18000000,"resetsAt":"2026-07-19T06:59:59.611Z"},
        {"type":"progress","label":"Weekly","used":27,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-24T16:59:59.611Z"},
        {"type":"progress","label":"Fable","used":52,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-24T16:59:59.612Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "claude")
	if err != nil {
		t.Fatal(err)
	}
	fable, ok := got.Allowance("model:fable:weekly")
	if !ok {
		t.Fatalf("allowances = %#v", got.Allowances)
	}
	assertClose(t, fable.Remaining, .48)
	if fable.Scope != "model" || fable.Role != "weekly" || fable.SourceLabel != "Fable" {
		t.Fatalf("fable allowance = %#v", fable)
	}
	if fable.PeriodDurationSeconds != 7*24*60*60 {
		t.Fatalf("period duration = %d", fable.PeriodDurationSeconds)
	}
	if _, ok := got.Allowance("session"); !ok {
		t.Fatalf("session missing from %#v", got.Allowances)
	}
	if _, ok := got.Allowance("weekly"); !ok {
		t.Fatalf("weekly missing from %#v", got.Allowances)
	}
}

func TestParseInfersMissingClaudeFableResetFromAccountWeeklyWindow(t *testing.T) {
	payload := `{
      "providerId":"claude",
      "fetchedAt":"2026-07-24T18:16:33.134Z",
      "lines":[
        {"type":"progress","label":"Fable","used":0,"limit":100,"periodDurationMs":604800000},
        {"type":"progress","label":"Session","used":0,"limit":100,"periodDurationMs":18000000,"resetsAt":"2026-07-24T22:00:00.306Z"},
        {"type":"progress","label":"Weekly","used":0,"limit":100,"periodDurationMs":604800000,"resetsAt":"2026-07-31T17:00:00.306Z"}
      ]
    }`

	got, err := openusage.Parse([]byte(payload), "claude")
	if err != nil {
		t.Fatal(err)
	}
	fable, ok := got.Allowance("model:fable:weekly")
	if !ok || fable.Remaining != 1 || !fable.ResetsAt.Equal(got.Weekly.ResetsAt) || !fable.ResetInferred {
		t.Fatalf("fable=%#v weekly=%#v ok=%v", fable, got.Weekly, ok)
	}
	if got.Confidence != "medium" {
		t.Fatalf("confidence = %q", got.Confidence)
	}
}

func TestParseAcceptsProviderWithoutShortWindow(t *testing.T) {
	payload := `[{"providerId":"codex","fetchedAt":"2026-07-16T18:00:00Z","lines":[
      {"type":"progress","label":"Weekly","used":33,"limit":100,"resetsAt":"2026-07-23T04:16:35Z"}
    ]}]`
	got, err := openusage.Parse([]byte(payload), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Short != nil {
		t.Fatalf("short window = %#v, want nil", got.Short)
	}
	assertClose(t, got.Weekly.Remaining, 0.67)
}

func TestParseRejectsMissingWeeklyWindow(t *testing.T) {
	payload := `[{"providerId":"codex","fetchedAt":"2026-07-16T18:00:00Z","lines":[]}]`
	if _, err := openusage.Parse([]byte(payload), "codex"); err == nil {
		t.Fatal("expected missing-window error")
	}
}

func TestParseIgnoresUnrelatedProgressLines(t *testing.T) {
	payload := `[{"providerId":"codex","fetchedAt":"2026-07-16T18:00:00Z","lines":[
      {"type":"progress","label":"Monthly spend","used":1,"limit":10},
      {"type":"progress","label":"Session","used":70,"limit":100,"resetsAt":"2026-07-16T22:00:00Z"},
      {"type":"progress","label":"Weekly","used":53,"limit":100,"resetsAt":"2026-07-17T05:00:00Z"}
    ]}]`

	if _, err := openusage.Parse([]byte(payload), "codex"); err != nil {
		t.Fatalf("unrelated progress line should be ignored: %v", err)
	}
}

func TestClientRejectsNonSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := openusage.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	if _, _, err := client.Fetch(context.Background(), "codex"); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestClientFetchesProviderEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage/codex" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()

	client := openusage.Client{BaseURL: server.URL, HTTPClient: server.Client()}
	got, _, err := client.Fetch(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertClose(t, got.Weekly.Remaining, 0.47)
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}
