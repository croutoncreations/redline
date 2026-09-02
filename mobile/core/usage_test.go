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
