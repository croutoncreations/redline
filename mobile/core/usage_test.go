package core_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jfox/redline/mobile/core"
)

// fixedNow is the clock every test renders against, so relative times are
// deterministic.
var fixedNow = time.Date(2026, 7, 20, 19, 0, 0, 0, time.UTC)

func usagePayload(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dashboard" {
			t.Errorf("path = %q, want /v1/dashboard", r.URL.Path)
		}
		// The read model carries every run and task; the capacity screen
		// renders neither, so asking for the whole thing would download tens
		// of times more data than it uses.
		if got := r.URL.Query().Get("fields"); got != "providers,health" {
			t.Errorf("fields = %q, want providers,health", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func decodeUsage(t *testing.T, raw string) core.UsageView {
	t.Helper()
	var view core.UsageView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode usage view: %v\nraw: %s", err, raw)
	}
	return view
}

// The core returns JSON rather than Go structs because gomobile can only bind
// simple types across the FFI boundary.
func TestFetchUsageReturnsRenderableJSON(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude", "paused": false,
			"snapshot_stale": false,
			"snapshot": {
				"provider": "claude",
				"observed_at": "2026-07-20T18:54:00Z",
				"short": {"remaining": 0.62, "resets_at": "2026-07-20T23:00:00Z"},
				"weekly": {"remaining": 0.53, "resets_at": "2026-07-24T19:00:00Z"},
				"source": "openusage"
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	if len(view.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(view.Providers))
	}
	provider := view.Providers[0]
	if provider.ID != "claude-main" || provider.Provider != "claude" {
		t.Fatalf("provider = %#v", provider)
	}
	if provider.Session == nil || provider.Session.RemainingPercent != 62 {
		t.Fatalf("session = %#v, want 62%%", provider.Session)
	}
	if provider.Weekly == nil || provider.Weekly.RemainingPercent != 53 {
		t.Fatalf("weekly = %#v, want 53%%", provider.Weekly)
	}
}

// The reset countdown is what the phone renders; computing it in Go keeps the
// arithmetic in one tested place instead of duplicated per platform.
func TestWindowReportsSecondsUntilReset(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 1, "resets_at": "2026-07-20T23:00:00Z"},
				"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"}
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	session := view.Providers[0].Session
	if session.ResetsInSeconds != int64(4*time.Hour/time.Second) {
		t.Fatalf("session resets in %ds, want %d", session.ResetsInSeconds, int64(4*time.Hour/time.Second))
	}
	weekly := view.Providers[0].Weekly
	if weekly.ResetsInSeconds != int64(4*24*time.Hour/time.Second) {
		t.Fatalf("weekly resets in %ds", weekly.ResetsInSeconds)
	}
}

// A reset already in the past must not render as a negative countdown.
func TestElapsedResetClampsToZero(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 0.2, "resets_at": "2026-07-20T18:00:00Z"},
				"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"}
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)
	if got := view.Providers[0].Session.ResetsInSeconds; got != 0 {
		t.Fatalf("elapsed reset = %d, want 0", got)
	}
}

// The dashboard already distinguishes these states and the app must too, so a
// stale or paused provider is never rendered as live data.
func TestProviderStatesAreSurfaced(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [
			{"id": "a", "provider": "claude", "paused": true,
			 "snapshot": {"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"}}},
			{"id": "b", "provider": "codex", "snapshot_stale": true,
			 "snapshot": {"weekly": {"remaining": 0.4, "resets_at": "2026-07-24T19:00:00Z"}}},
			{"id": "c", "provider": "codex", "error": "no usage snapshot yet"}
		]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	if !view.Providers[0].Paused {
		t.Error("provider a should be paused")
	}
	if !view.Providers[1].Stale {
		t.Error("provider b should be stale")
	}
	if view.Providers[2].Error == "" {
		t.Error("provider c should carry its error")
	}
	if view.Providers[2].Weekly != nil {
		t.Error("provider c has no snapshot, so it must not render a weekly window")
	}
}

// Model-scope pools (for example Claude's Fable allowance) are shown alongside
// the account windows, mirroring the web dashboard.
func TestModelPoolsAreIncludedWithoutDuplicatingAccountWindows(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 1, "resets_at": "2026-07-20T23:00:00Z"},
				"weekly": {"remaining": 0.93, "resets_at": "2026-07-24T19:00:00Z"},
				"allowances": [
					{"key": "session", "source_label": "Session", "scope": "account", "role": "short",
					 "remaining": 1, "resets_at": "2026-07-20T23:00:00Z"},
					{"key": "weekly", "source_label": "Weekly", "scope": "account", "role": "weekly",
					 "remaining": 0.93, "resets_at": "2026-07-24T19:00:00Z"},
					{"key": "model:fable:weekly", "source_label": "Fable", "scope": "model", "role": "weekly",
					 "remaining": 1, "resets_at": "2026-07-24T19:00:00Z", "reset_inferred": true}
				]
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	pools := view.Providers[0].Pools
	// The canonical session/weekly pools are already rendered as dedicated
	// windows; repeating them as pools is the duplicate-meter bug the web
	// dashboard had.
	if len(pools) != 1 {
		t.Fatalf("pools = %#v, want only the model pool", pools)
	}
	if pools[0].Label != "Fable" {
		t.Fatalf("pool label = %q, want Fable", pools[0].Label)
	}
	if !pools[0].ResetInferred {
		t.Error("inferred reset must be marked so notifications can skip it")
	}
}

func TestFetchUsageSurfacesAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"Redline API authentication is required"}`)
	}))
	defer server.Close()

	client := core.NewClientWithClock(server.URL, "wrong", func() time.Time { return fixedNow })
	_, err := client.FetchUsage()
	if err == nil {
		t.Fatal("expected an error for a 401")
	}
	if !core.IsUnauthorized(err) {
		t.Fatalf("error %v should be recognisable as unauthorized so the UI can prompt to re-pair", err)
	}
}

func TestFetchUsageSurfacesUnreachableHost(t *testing.T) {
	// Port 1 is reserved and refuses connections.
	client := core.NewClientWithClock("http://127.0.0.1:1", "test-token", func() time.Time { return fixedNow })
	_, err := client.FetchUsage()
	if err == nil {
		t.Fatal("expected an error when the desktop is unreachable")
	}
	if core.IsUnauthorized(err) {
		t.Fatal("an unreachable host must not be reported as an auth failure")
	}
}

// A collector may publish a window with no reset time. Both windows must fall
// back to the canonical allowance rather than rendering an empty meter, and
// they must behave the same way as each other.
func TestWindowsFallBackToAllowancesWhenResetIsMissing(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 0, "resets_at": "0001-01-01T00:00:00Z"},
				"weekly": {"remaining": 0, "resets_at": "0001-01-01T00:00:00Z"},
				"allowances": [
					{"key": "session", "source_label": "Session", "scope": "account", "role": "short",
					 "remaining": 0.44, "resets_at": "2026-07-20T23:00:00Z"},
					{"key": "weekly", "source_label": "Weekly", "scope": "account", "role": "weekly",
					 "remaining": 0.71, "resets_at": "2026-07-24T19:00:00Z"}
				]
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	provider := view.Providers[0]
	if provider.Session == nil || provider.Session.RemainingPercent != 44 {
		t.Fatalf("session = %#v, want the session allowance at 44%%", provider.Session)
	}
	if provider.Weekly == nil || provider.Weekly.RemainingPercent != 71 {
		t.Fatalf("weekly = %#v, want the weekly allowance at 71%%", provider.Weekly)
	}
	// Having been promoted to windows, they must not also appear as pools.
	if len(provider.Pools) != 0 {
		t.Fatalf("pools = %#v, want none", provider.Pools)
	}
}

// Collectors emit "session" and "weekly" today, but the domain model allows any
// key. An account-scoped short/weekly pool under a different key is still a
// duplicate of a dedicated window and must not be rendered twice.
func TestAccountScopedPoolsAreDedupedEvenUnderAnUnexpectedKey(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 1, "resets_at": "2026-07-20T23:00:00Z"},
				"weekly": {"remaining": 0.9, "resets_at": "2026-07-24T19:00:00Z"},
				"allowances": [
					{"key": "account:primary:weekly", "source_label": "Primary", "scope": "account",
					 "role": "weekly", "remaining": 0.9, "resets_at": "2026-07-24T19:00:00Z"},
					{"key": "model:fable:weekly", "source_label": "Fable", "scope": "model",
					 "role": "weekly", "remaining": 1, "resets_at": "2026-07-24T19:00:00Z"}
				]
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	pools := view.Providers[0].Pools
	if len(pools) != 1 || pools[0].Label != "Fable" {
		t.Fatalf("pools = %#v, want only the model-scoped Fable pool", pools)
	}
}

// NewClient is the constructor the gomobile binding actually exposes;
// NewClientWithClock takes a func and is dropped from the binding, so without
// this the shipped path would have no coverage.
func TestNewClientIsTheBoundConstructorAndWorks(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{"id": "claude-main", "provider": "claude",
			"snapshot": {"weekly": {"remaining": 0.5, "resets_at": "2126-07-24T19:00:00Z"}}}]
	}`)

	client := core.NewClient(server.URL+"/", "test-token")
	if client.BaseURL() != server.URL {
		t.Fatalf("BaseURL() = %q, want the trailing slash trimmed", client.BaseURL())
	}
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)
	if view.Providers[0].Weekly.RemainingPercent != 50 {
		t.Fatalf("weekly = %#v", view.Providers[0].Weekly)
	}
	// The reset is far in the future, so a real clock must still count down.
	if view.Providers[0].Weekly.ResetsInSeconds <= 0 {
		t.Fatal("NewClient should use a real clock and produce a positive countdown")
	}
}

// Tokens arrive from files, clipboards, and QR scans, so they carry stray
// whitespace. An untrimmed newline produces an invalid header value, which
// surfaces as a baffling transport error instead of an auth failure.
func TestNewClientTrimsWhitespaceFromToken(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := core.NewClient(server.URL, "secret-token\n")
	if _, err := client.FetchRuns(); err != nil {
		t.Fatalf("a token with a trailing newline must still work: %v", err)
	}
	if seen != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want the trimmed token", seen)
	}
}

// The web dashboard shows "openusage source · 0/1 active · sampled 4 mins ago"
// under each provider. That line answers where the numbers came from and how
// much to trust them, which matters when two surfaces disagree.
func TestFetchUsageReportsProvenance(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"providers":[{
	  "id":"claude-main","provider":"claude","paused":false,"snapshot_stale":false,
	  "usage_source":{"active":"openusage","consecutive_failures":0},"active_runs":0,"max_concurrent_runs":1,
	  "snapshot":{"provider":"claude","observed_at":%q,"source":"openusage",
	    "weekly":{"remaining":80,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339),
		now.Add(-4*time.Minute).Format(time.RFC3339),
		now.Add(48*time.Hour).Format(time.RFC3339))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	client := core.NewClientWithClock(server.URL, "token", func() time.Time { return now })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	var view core.UsageView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	provider := view.Providers[0]
	if provider.SourceLabel != "openusage source \u00b7 0/1 active \u00b7 sampled 4m ago" {
		t.Errorf("source label = %q", provider.SourceLabel)
	}
}

// OpenUsage can report the five hour window without a reset time while
// provider state is refreshing, and the collector drops it rather than
// inventing a reset. The phone then had nothing to show and silently omitted
// the row, so the most-watched number on the screen disappeared while the
// header still said "Live".
//
// A row that says it does not know is honest. A missing row looks like the
// limit does not exist, which is the wrong conclusion to hand someone deciding
// whether to start a run.
func TestAMissingShortWindowIsReportedRatherThanHidden(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 57, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"providers":[{
	  "id":"claude-main","provider":"claude","paused":false,
	  "active_runs":1,"max_concurrent_runs":1,
	  "usage_source":{"active":"openusage","consecutive_failures":0},
	  "snapshot":{"provider":"claude","observed_at":%q,"source":"openusage","confidence":"medium","short_window_unavailable":true,
	    "weekly":{"remaining":0.54,"resets_at":%q},
	    "allowances":[{"key":"weekly","source_label":"Weekly","scope":"account","role":"weekly","remaining":0.54,"resets_at":%q}]}}]}`,
		now.Format(time.RFC3339), now.Add(-4*time.Minute).Format(time.RFC3339),
		now.Add(26*time.Hour).Format(time.RFC3339), now.Add(26*time.Hour).Format(time.RFC3339))

	view := fetchView(t, payload, now)
	provider := view.Providers[0]

	if provider.Session != nil {
		t.Fatal("a window with no reset must not be invented as a real one")
	}
	// The screen needs to know the difference between "no such limit" and
	// "this limit exists and we cannot read it right now".
	if !provider.SessionUnknown {
		t.Fatal("the missing five hour window should be flagged as unknown")
	}
	// The weekly numbers are still good and must not be discarded with it.
	if provider.Weekly == nil || provider.Weekly.RemainingPercent != 54 {
		t.Fatalf("weekly was lost: %#v", provider.Weekly)
	}
}

// A provider that genuinely has no five hour limit must not grow a phantom
// "unknown" row. Codex is exactly this case: weekly only.
func TestAProviderWithNoShortWindowIsNotFlagged(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 57, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"providers":[{
	  "id":"codex-main","provider":"codex","paused":false,
	  "usage_source":{"active":"openusage","consecutive_failures":0},
	  "snapshot":{"provider":"codex","observed_at":%q,"source":"openusage","confidence":"high",
	    "weekly":{"remaining":0,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339), now.Add(96*time.Hour).Format(time.RFC3339))

	if fetchView(t, payload, now).Providers[0].SessionUnknown {
		t.Fatal("codex has no five hour window and must not be flagged as unknown")
	}
}

// When the window is present the flag must stay off, or every screen would
// carry a permanent warning.
func TestAPresentShortWindowIsNotFlagged(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 57, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"providers":[{
	  "id":"claude-main","provider":"claude","paused":false,
	  "usage_source":{"active":"openusage","consecutive_failures":0},
	  "snapshot":{"provider":"claude","observed_at":%q,"source":"openusage","confidence":"high",
	    "short":{"remaining":0.21,"resets_at":%q},
	    "weekly":{"remaining":0.54,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		now.Add(3*time.Hour).Format(time.RFC3339), now.Add(26*time.Hour).Format(time.RFC3339))

	provider := fetchView(t, payload, now).Providers[0]
	if provider.SessionUnknown {
		t.Fatal("a present window must not be flagged unknown")
	}
	if provider.Session == nil || provider.Session.RemainingPercent != 21 {
		t.Fatalf("session: %#v", provider.Session)
	}
}

// fetchView serves one payload and returns the rendered capacity screen.
func fetchView(t *testing.T, payload string, now time.Time) core.UsageView {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	client := core.NewClientWithClock(server.URL, "token", func() time.Time { return now })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	var view core.UsageView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(view.Providers) == 0 {
		t.Fatal("no providers in the rendered view")
	}
	return view
}

// A provider the user has paused must say so: it explains why nothing is
// dispatching, and it is the thing they would want to undo.
func TestFetchUsageReportsPaused(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"providers":[{
	  "id":"codex-main","provider":"codex","paused":true,"usage_source":{"active":"native","consecutive_failures":0},
	  "active_runs":1,"max_concurrent_runs":2,
	  "snapshot":{"provider":"codex","observed_at":%q,"source":"native",
	    "weekly":{"remaining":0,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339),
		now.Format(time.RFC3339),
		now.Add(96*time.Hour).Format(time.RFC3339))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	client := core.NewClientWithClock(server.URL, "token", func() time.Time { return now })
	raw, _ := client.FetchUsage()
	var view core.UsageView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Providers[0].Paused {
		t.Error("a paused provider must be marked")
	}
	if !strings.Contains(view.Providers[0].SourceLabel, "1/2 active") {
		t.Errorf("source label = %q, want the active run count", view.Providers[0].SourceLabel)
	}
}

// The capacity payload already carries health, and the header shows a health
// pill, so carrying it through avoids a second request for something the app
// has already been sent.
func TestFetchUsageCarriesHealth(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"generated_at":%q,"health":{"status":"degraded","failed_runs":3,
	  "dispatch_errors":1},"providers":[{"id":"claude-main","provider":"claude",
	  "usage_source":{"active":"openusage"},"active_runs":0,"max_concurrent_runs":1,
	  "snapshot":{"provider":"claude","observed_at":%q,"source":"openusage",
	    "weekly":{"remaining":0.8,"resets_at":%q}}}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		now.Add(48*time.Hour).Format(time.RFC3339))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	client := core.NewClientWithClock(server.URL, "token", func() time.Time { return now })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	var view core.UsageView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Health == nil {
		t.Fatal("health must be carried through")
	}
	if !view.Health.Degraded {
		t.Error("a degraded scheduler must be marked")
	}
	if view.Health.Detail != "3 failed \u00b7 1 dispatch errors" {
		t.Errorf("detail = %q", view.Health.Detail)
	}
}

// The header needs to know how old the numbers are, not just whether the
// connection is up.
//
// The stream pushes every five seconds while the collector polls the provider
// every five minutes, so "live" sat above four-minute-old data and read as a
// lie. Both facts were true; the screen showed only the connection.
//
// Exposed as seconds rather than a formatted string so the app can decide when
// staleness is worth mentioning, and so the wording lives with the UI.
func TestUsageViewReportsHowOldTheNumbersAre(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"providers":[{"provider":"claude","snapshot":{
"provider":"claude","observed_at":%q,
"weekly":{"remaining":0.5,"resets_at":%q},
"source":"openusage","confidence":"high"}}]}`,
		now.Add(-4*time.Minute).Format(time.RFC3339),
		now.Add(48*time.Hour).Format(time.RFC3339))

	rendered, err := renderThroughClient(t, payload, now)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var view struct {
		SampledAgeSeconds int `json:"sampled_age_seconds"`
	}
	if err := json.Unmarshal([]byte(rendered), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.SampledAgeSeconds != 240 {
		t.Errorf("sampled_age_seconds = %d, want 240", view.SampledAgeSeconds)
	}
}

// With several providers the age is the oldest of them: the header speaks for
// the whole screen, and claiming the freshest would overstate the rest.
func TestUsageViewAgeIsTheOldestProvider(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload := fmt.Sprintf(`{"providers":[
{"provider":"claude","snapshot":{"provider":"claude","observed_at":%q,
"weekly":{"remaining":0.5,"resets_at":%q},"source":"openusage","confidence":"high"}},
{"provider":"codex","snapshot":{"provider":"codex","observed_at":%q,
"weekly":{"remaining":0.5,"resets_at":%q},"source":"openusage","confidence":"high"}}]}`,
		now.Add(-30*time.Second).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339),
		now.Add(-6*time.Minute).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339))

	rendered, err := renderThroughClient(t, payload, now)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var view struct {
		SampledAgeSeconds int `json:"sampled_age_seconds"`
	}
	if err := json.Unmarshal([]byte(rendered), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.SampledAgeSeconds != 360 {
		t.Errorf("sampled_age_seconds = %d, want 360 (the oldest)", view.SampledAgeSeconds)
	}
}

// renderThroughClient drives the real FetchUsage against a stub desktop, so
// these tests exercise the shipped path rather than a test-only entry point.
func renderThroughClient(t *testing.T, payload string, now time.Time) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)

	client := core.NewClientWithClock(server.URL, "token", func() time.Time { return now })
	return client.FetchUsage()
}

// A bar of what is left says nothing about whether that is a lot or a little.
// Halfway through the window, half left is exactly on pace; with an hour to go
// it is comfortable; in the first ten minutes it is a problem. The window's
// elapsed fraction is what makes the remaining fraction readable, so it
// travels with it. Computed in Go, like the countdown, so both platforms draw
// the same mark for the same snapshot.
func TestWindowReportsHowMuchOfItHasElapsed(t *testing.T) {
	// fixedNow is 19:00. Session resets at 23:00, so it began at 18:00 and is
	// one fifth through; the week resets in four days, so it is three sevenths
	// through.
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 1, "resets_at": "2026-07-20T23:00:00Z"},
				"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"}
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	if got := view.Providers[0].Session.ElapsedPercent; got != 20 {
		t.Errorf("session elapsed = %d%%, want 20 (1h of a 5h window)", got)
	}
	if got := view.Providers[0].Weekly.ElapsedPercent; got != 43 {
		t.Errorf("weekly elapsed = %d%%, want 43 (3d of 7d)", got)
	}
}

// A model pool's period comes from the allowance itself, which is the only
// place a non-standard length can be stated.
func TestPoolElapsedUsesTheAllowancePeriod(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "codex-main", "provider": "codex",
			"snapshot": {
				"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"},
				"allowances": [{
					"key": "spark", "source_label": "Spark", "scope": "model", "role": "short",
					"remaining": 1, "resets_at": "2026-07-21T01:00:00Z",
					"period_duration_seconds": 86400
				}]
			}
		}]
	}`)

	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)

	var spark *core.Pool
	for i := range view.Providers[0].Pools {
		if view.Providers[0].Pools[i].Key == "spark" {
			spark = &view.Providers[0].Pools[i]
		}
	}
	if spark == nil {
		t.Fatal("spark pool missing")
	}
	// Resets 01:00 tomorrow with a 24h period: began 01:00 today; at 19:00
	// that is 18h in, 75%.
	if spark.ElapsedPercent != 75 {
		t.Errorf("spark elapsed = %d%%, want 75", spark.ElapsedPercent)
	}
}

// Past the reset, the window is over; the tick sits at the end rather than
// running off it.
func TestElapsedClampsAtTheReset(t *testing.T) {
	server := usagePayload(t, `{
		"generated_at": "2026-07-20T19:00:00Z",
		"providers": [{
			"id": "claude-main", "provider": "claude",
			"snapshot": {
				"short": {"remaining": 0.2, "resets_at": "2026-07-20T18:00:00Z"},
				"weekly": {"remaining": 0.5, "resets_at": "2026-07-24T19:00:00Z"}
			}
		}]
	}`)
	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeUsage(t, raw).Providers[0].Session.ElapsedPercent; got != 100 {
		t.Errorf("elapsed past reset = %d%%, want 100", got)
	}
}
