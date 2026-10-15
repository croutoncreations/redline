package resetsignal

import (
	"context"
	"io"
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
