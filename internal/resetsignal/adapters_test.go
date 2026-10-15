package resetsignal

import (
	"context"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 10, 13, 4, 20, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNormalizeCurrentSchemasAndAmbiguousWindow(t *testing.T) {
	resetBeacon, err := normalizeResetBeacon(fixture(t, "resetbeacon-scheduled.json"), nil, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC)
	if resetBeacon.Kind != KindScheduled || resetBeacon.Freshness != Fresh || resetBeacon.Window.End == nil ||
		!resetBeacon.Window.End.Equal(deadline) || resetBeacon.Window.Certainty != WindowMirrorInterpretation {
		t.Fatalf("reset beacon = %+v", resetBeacon)
	}

	willReset, err := normalizeWillReset(fixture(t, "willreset-promised.json"), nil, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if willReset.Kind != KindPromised || willReset.Freshness != MissingTTL || value(willReset.Prediction24Percent) != 19 {
		t.Fatalf("will reset = %+v", willReset)
	}

	header := http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}
	codexReset, err := normalizeCodexReset(fixture(t, "codex-reset-null-window.json"), header, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if codexReset.Kind != KindPromised || codexReset.Freshness != Fresh || codexReset.Window.Certainty != WindowMissing || value(codexReset.Prediction48Percent) != 34 {
		t.Fatalf("codex reset = %+v", codexReset)
	}
}

func TestFreshnessFutureStaleAndMissingTTL(t *testing.T) {
	future := fixtureNow.Add(6 * time.Minute)
	past := fixtureNow.Add(-time.Minute)
	if got := freshness(fixtureNow, &future, nil, nil, false); got != FutureDated {
		t.Fatalf("future = %s", got)
	}
	if got := freshness(fixtureNow, nil, &past, nil, false); got != Stale {
		t.Fatalf("stale = %s", got)
	}
	if got := freshness(fixtureNow, nil, nil, nil, false); got != MissingTTL {
		t.Fatalf("missing = %s", got)
	}
}

func TestCanonicalOriginalRejectsUntrustedURLs(t *testing.T) {
	for _, raw := range []string{"http://x.com/a/status/1", "https://evil.example/a/status/1", "https://x.com/a/not-status/1", "https://user@x.com/a/status/1"} {
		if got, _ := canonicalOriginal(raw); got != "" {
			t.Fatalf("accepted %q as %q", raw, got)
		}
	}
	got, account := canonicalOriginal("https://twitter.com/thsottiaux/status/2109812360596865224?ref=x#fragment")
	if got != "https://x.com/thsottiaux/status/2109812360596865224" || account != "thsottiaux" {
		t.Fatalf("canonical = %q account=%q", got, account)
	}
}

func TestInvalidTrackerPredictionIsOmitted(t *testing.T) {
	value := 101.0
	signal := Signal{Prediction24Percent: &value}
	validateSignal(&signal, fixtureNow)
	if signal.Prediction24Percent != nil || len(signal.Warnings) == 0 {
		t.Fatalf("signal = %+v", signal)
	}
}

func TestFetcherUsesGETIdentityCapAndRetryMetadata(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "redline-reset-preview/test (+https://example.test)" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("request = %s ua=%q accept=%q", request.Method, request.Header.Get("User-Agent"), request.Header.Get("Accept"))
		}
		if calls == 1 {
			return response(http.StatusTooManyRequests, "application/json", `{}`, http.Header{"Retry-After": {"0"}}), nil
		}
		return response(http.StatusOK, "application/json", `{"ok":true}`, nil), nil
	})
	fetcher := Fetcher{
		HTTP: &http.Client{Transport: transport}, Timeout: time.Second, MaxBody: 64,
		UserAgent: "redline-reset-preview/test (+https://example.test)", Now: func() time.Time { return fixtureNow },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	document, fetchErr := fetcher.get(context.Background(), "test", "https://example.test/feed")
	if fetchErr != nil || string(document.Body) != `{"ok":true}` || document.Metadata.Attempts != 2 || document.Metadata.BackoffApplied != defaultBackoff {
		t.Fatalf("document=%+v err=%+v", document, fetchErr)
	}

	oversized := fetcher
	oversized.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "application/json", strings.Repeat("x", 65), nil), nil
	})}
	if _, got := oversized.get(context.Background(), "test", "https://example.test/feed"); got == nil || got.Code != "oversized" {
		t.Fatalf("oversized error = %+v", got)
	}

	notJSON := fetcher
	notJSON.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", `{}`, nil), nil
	})}
	if _, got := notJSON.get(context.Background(), "test", "https://example.test/feed"); got == nil || got.Code != "not_json" {
		t.Fatalf("not-json error = %+v", got)
	}
	if _, got := fetcher.get(context.Background(), "test", "http://example.test/feed"); got == nil || got.Code != "invalid_url" {
		t.Fatalf("URL error = %+v", got)
	}
}

func TestFetcherNeverShortensRetryAfterOrViolatesCodexReadInterval(t *testing.T) {
	for name, test := range map[string]struct {
		feed       string
		retryAfter string
		wantDelay  time.Duration
	}{
		"sixty seconds": {feed: "resetbeacon", retryAfter: "60", wantDelay: time.Minute},
		"future date":   {feed: "resetbeacon", retryAfter: fixtureNow.Add(time.Minute).Format(http.TimeFormat), wantDelay: time.Minute},
		"codex floor":   {feed: "codex-reset", retryAfter: "", wantDelay: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			calls, sleeps := 0, 0
			fetcher := Fetcher{
				HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return response(http.StatusTooManyRequests, "application/json", `{}`, http.Header{"Retry-After": {test.retryAfter}}), nil
				})},
				Timeout: 5 * time.Second, Now: func() time.Time { return fixtureNow },
				Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
			}
			_, fetchErr := fetcher.get(context.Background(), test.feed, "https://example.test/feed")
			if fetchErr == nil || fetchErr.Code != "rate_limited" || fetchErr.Metadata.BackoffApplied != test.wantDelay || calls != 1 || sleeps != 0 {
				t.Fatalf("err=%+v calls=%d sleeps=%d", fetchErr, calls, sleeps)
			}
			if test.retryAfter != "" && fetchErr.Metadata.RetryAfter != time.Minute {
				t.Fatalf("retry-after = %s", fetchErr.Metadata.RetryAfter)
			}
		})
	}
}

func TestFetcherInvalidRetryAfterUsesBoundedBackoffAndReportsInterruptedWait(t *testing.T) {
	for _, header := range []string{"-1", "not-a-date", ""} {
		calls := 0
		fetcher := Fetcher{
			HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return response(http.StatusServiceUnavailable, "application/json", `{}`, http.Header{"Retry-After": {header}}), nil
				}
				return response(http.StatusOK, "application/json", `{}`, nil), nil
			})},
			Timeout: time.Second, Now: func() time.Time { return fixtureNow },
			Sleep: func(context.Context, time.Duration) error { return nil },
		}
		document, fetchErr := fetcher.get(context.Background(), "resetbeacon", "https://example.test/feed")
		if fetchErr != nil || document.Metadata.Attempts != 2 || document.Metadata.BackoffApplied != defaultBackoff {
			t.Fatalf("header=%q document=%+v err=%+v", header, document, fetchErr)
		}
	}

	fetcher := Fetcher{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusServiceUnavailable, "application/json", `{}`, nil), nil
		})},
		Timeout: time.Second, Now: func() time.Time { return fixtureNow },
		Sleep: func(context.Context, time.Duration) error { return context.Canceled },
	}
	if _, fetchErr := fetcher.get(context.Background(), "resetbeacon", "https://example.test/feed"); fetchErr == nil || fetchErr.Code != "retry_deferred" || fetchErr.Metadata.Attempts != 1 {
		t.Fatalf("interrupted wait = %+v", fetchErr)
	}
}

func TestFetcherTimeoutIsBounded(t *testing.T) {
	fetcher := Fetcher{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
		Timeout: 5 * time.Millisecond,
	}
	if _, fetchErr := fetcher.get(context.Background(), "resetbeacon", "https://example.test/feed"); fetchErr == nil || fetchErr.Code != "fetch" || fetchErr.Metadata.Attempts != 1 {
		t.Fatalf("timeout error = %+v", fetchErr)
	}
}

func TestFetchAllContainsDecodeErrors(t *testing.T) {
	fetcher := Fetcher{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "application/json", `{not json`, nil), nil
	})}, Timeout: time.Second, Now: func() time.Time { return fixtureNow }}
	results := FetchAll(context.Background(), fetcher, []Source{{Name: "broken", Endpoint: "https://example.test/feed", Normalize: normalizeWillReset}})
	if len(results) != 1 || results[0].Error == nil || results[0].Error.Code != "decode" {
		t.Fatalf("results = %+v", results)
	}
}

func TestFetcherDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	fetcher := Fetcher{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				t.Fatal("followed redirect")
			}
			return response(http.StatusFound, "application/json", `{}`, http.Header{"Location": {"https://untrusted.example/feed"}}), nil
		})},
		Timeout: time.Second,
	}
	_, fetchErr := fetcher.get(context.Background(), "resetbeacon", "https://example.test/feed")
	if fetchErr == nil || fetchErr.Code != "http_status" || calls != 1 {
		t.Fatalf("err=%+v calls=%d", fetchErr, calls)
	}
}

func TestLatestAlertOnlyCorrectsMatchingAnnouncement(t *testing.T) {
	for name, alertID := range map[string]string{"matching": "one", "unrelated": "two"} {
		t.Run(name, func(t *testing.T) {
			body := `{
				"mode":"announced","updated_at":"2026-10-13T04:14:42Z",
				"official_signal":{"tweet_id":"one","summary":"promise","at":"2026-10-13T01:03:59Z","url":"https://x.com/a/status/one","signal_type":"promise","window":null},
				"latest_alert":{"id":"` + alertID + `","state":"withdrawn","corrected":true,"url":"https://x.com/a/status/` + alertID + `"}
			}`
			signal, err := normalizeCodexReset([]byte(body), http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			wantCorrected := name == "matching"
			if signal.Corrected != wantCorrected || signal.Withdrawn != wantCorrected {
				t.Fatalf("signal = %+v", signal)
			}
		})
	}
}

// codex-reset's live forecast carries no official signal and a confirmed
// reset alert. That is a completion aged by the alert's source time, not a
// withdrawn record.
func TestCodexResetConfirmedAlertIsACompletion(t *testing.T) {
	now := time.Date(2026, 10, 15, 6, 31, 0, 0, time.UTC)
	header := http.Header{"X-Published-Expires-At": {"2026-10-15T06:32:00Z"}}
	signal, err := normalizeCodexReset(fixture(t, "codex-reset-confirmed-alert.json"), header, now)
	if err != nil {
		t.Fatal(err)
	}
	sourceAt := time.Date(2026, 10, 15, 6, 13, 1, 0, time.UTC)
	if signal.Kind != KindCompleted || signal.Withdrawn || signal.Corrected || signal.PublishedAt == nil || !signal.PublishedAt.Equal(sourceAt) {
		t.Fatalf("signal = %+v", signal)
	}
	signal.Feed = "codex-reset"
	report := BuildReport(now, []SourceResult{{Signal: &signal}})
	if report.State != KindCompleted {
		t.Fatalf("report = %+v", report)
	}
	// Past the 72-hour horizon the retained alert is historical.
	later := sourceAt.Add(73 * time.Hour)
	if isCurrent(signal, later) {
		t.Fatal("a 73-hour-old alert is still current")
	}
}

// The forecast's ordinary snapshot is a model prediction with probabilities.
func TestCodexResetModelOnlySnapshotIsAPrediction(t *testing.T) {
	header := http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}
	signal, err := normalizeCodexReset(fixture(t, "codex-reset-model-only.json"), header, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Kind != KindPrediction || value(signal.Prediction24Percent) != 18 {
		t.Fatalf("signal = %+v", signal)
	}
	signal.Feed = "codex-reset"
	if report := BuildReport(fixtureNow, []SourceResult{{Signal: &signal}}); report.State != KindPrediction {
		t.Fatalf("report = %+v", report)
	}
}

// Alert states: active leaves the claim alone, confirmed completes a reset,
// explicit retractions and unrecognized states withdraw it (fail closed).
func TestCodexResetAlertStates(t *testing.T) {
	for state, want := range map[string]struct {
		kind      Kind
		withdrawn bool
	}{
		"active":     {KindPromised, false},
		"confirmed":  {KindCompleted, false},
		"withdrawn":  {KindPromised, true},
		"superseded": {KindPromised, true},
		"mystery":    {KindPromised, true},
	} {
		t.Run(state, func(t *testing.T) {
			body := `{
				"mode":"announced","updated_at":"2026-10-13T04:14:42Z",
				"official_signal":{"tweet_id":"one","summary":"promise","at":"2026-10-13T01:03:59Z","url":"https://x.com/a/status/one","signal_type":"promise","window":null},
				"latest_alert":{"id":"one","kind":"reset","state":"` + state + `","url":"https://x.com/a/status/one"}
			}`
			signal, err := normalizeCodexReset([]byte(body), http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if signal.Kind != want.kind || signal.Withdrawn != want.withdrawn {
				t.Fatalf("signal = %+v", signal)
			}
			if unknown := contains(signal.Warnings, "unrecognized latest alert state"); unknown != (state == "mystery") {
				t.Fatalf("unrecognized-state warning = %v, warnings %v", unknown, signal.Warnings)
			}
		})
	}
}

func codexForecast(official, banked, alert string) string {
	return `{"mode":"model","updated_at":"2026-10-13T04:14:42Z","banked_state":` + banked + `,
		"probabilities":{"rounded_24h":18,"rounded_48h":33},"official_signal":` + official + `,"latest_alert":` + alert + `}`
}

// An alert older than the 72-hour horizon, future-dated, or undated must not
// age or replace the snapshot's live probabilities.
func TestCodexResetStaleAlertDoesNotHideLivePrediction(t *testing.T) {
	header := http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}
	for name, sourceAt := range map[string]string{
		"four days old": `"2026-10-09T04:00:00Z"`,
		"future":        `"2026-10-14T04:00:00Z"`,
		"undated":       `""`,
	} {
		t.Run(name, func(t *testing.T) {
			alert := `{"id":"9","kind":"reset","state":"active","source_at":` + sourceAt + `,"url":"https://x.com/a/status/9"}`
			signal, err := normalizeCodexReset([]byte(codexForecast("null", "null", alert)), header, fixtureNow)
			if err != nil {
				t.Fatal(err)
			}
			if signal.Kind != KindPrediction || signal.AnnouncementID != "" || signal.PublishedAt != nil || signal.Freshness != Fresh ||
				!contains(signal.Warnings, "latest alert not used as the announcement") {
				t.Fatalf("signal = %+v", signal)
			}
			signal.Feed = "codex-reset"
			report := BuildReport(fixtureNow, []SourceResult{{Signal: &signal}})
			if report.State != KindPrediction || len(report.CurrentPredictions) != 1 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// A confirmed reset alert never overwrites a banked-reset record, and a
// confirmed alert of another kind changes nothing but says so.
func TestCodexResetConfirmedAlertKeepsBankedAndIgnoresOtherKinds(t *testing.T) {
	header := http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}
	recent := `"2026-10-13T03:00:00Z"`
	banked, err := normalizeCodexReset([]byte(codexForecast("null", `"available"`,
		`{"id":"9","kind":"reset","state":"confirmed","source_at":`+recent+`,"url":"https://x.com/a/status/9"}`)), header, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if banked.Kind != KindBanked || banked.Withdrawn || !contains(banked.Warnings, "did not replace the banked-reset record") {
		t.Fatalf("banked = %+v", banked)
	}
	boost, err := normalizeCodexReset([]byte(codexForecast("null", "null",
		`{"id":"9","kind":"boost","state":"confirmed","source_at":`+recent+`,"url":"https://x.com/a/status/9"}`)), header, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if boost.Kind != KindPrediction || boost.Withdrawn || !contains(boost.Warnings, `kind "boost" did not change the claim`) {
		t.Fatalf("boost = %+v", boost)
	}
}

// An active alert edited by the tracker (corrected: true) does not retract
// the current announcement; only a retracting state does.
func TestCodexResetCorrectedActiveAlertLeavesClaimUnchanged(t *testing.T) {
	body := `{
		"mode":"announced","updated_at":"2026-10-13T04:14:42Z",
		"official_signal":{"tweet_id":"one","summary":"promise","at":"2026-10-13T01:03:59Z","url":"https://x.com/a/status/one","signal_type":"promise","window":null},
		"latest_alert":{"id":"one","kind":"watch","state":"active","corrected":true,"url":"https://x.com/a/status/one"}
	}`
	signal, err := normalizeCodexReset([]byte(body), http.Header{"X-Published-Expires-At": {"2026-10-13T04:30:00Z"}}, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if signal.Corrected || signal.Withdrawn || signal.Kind != KindPromised || !contains(signal.Warnings, "edited by the tracker") {
		t.Fatalf("signal = %+v", signal)
	}
	signal.Feed = "codex-reset"
	if report := BuildReport(fixtureNow, []SourceResult{{Signal: &signal}}); report.State != KindPromised {
		t.Fatalf("report = %+v", report)
	}
}

// A Retry-After too large for a Duration must not overflow into a negative
// delay that the floor would shorten into an early retry.
func TestRetryAfterOverflowSaturates(t *testing.T) {
	for _, header := range []string{"10000000000", "99999999999999999999"} {
		delay, ok := retryDelay(header, fixtureNow)
		if !ok || delay != time.Duration(math.MaxInt64) {
			t.Fatalf("Retry-After %s = %s, %v", header, delay, ok)
		}
	}
	calls, sleeps := 0, 0
	fetcher := Fetcher{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return response(http.StatusTooManyRequests, "application/json", `{}`, http.Header{"Retry-After": {"10000000000"}}), nil
		})},
		Timeout: time.Second, Now: func() time.Time { return fixtureNow },
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	}
	_, fetchErr := fetcher.get(context.Background(), "resetbeacon", "https://example.test/feed")
	if fetchErr == nil || fetchErr.Code != "rate_limited" || calls != 1 || sleeps != 0 || fetchErr.Metadata.RetryAfter != time.Duration(math.MaxInt64) {
		t.Fatalf("err=%+v calls=%d sleeps=%d", fetchErr, calls, sleeps)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(status int, contentType, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", contentType)
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func value(number *float64) float64 {
	if number == nil {
		return -1
	}
	return *number
}
