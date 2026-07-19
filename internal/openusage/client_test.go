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
