package nativeusage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/nativeusage"
)

const claudeWindows = `"five_hour":{"utilization":10,"resets_at":"2026-09-24T20:00:00Z"},"seven_day":{"utilization":50,"resets_at":"2026-09-25T17:00:00Z"}`

var bankedNow = time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC)

func claudeServer(t *testing.T, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	seen := &http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = *r.Clone(context.Background())
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func claudeClient(server *httptest.Server) nativeusage.Client {
	return nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "claude-token"},
		ClaudeUsageURL: server.URL + "/api/oauth/usage", Now: func() time.Time { return bankedNow }}
}

func TestCodexBankedResetsSkipExpiredCredits(t *testing.T) {
	server, _ := claudeServer(t, `{"available_count":3,"credits":[
		{"id":"expired","expires_at":"2026-09-24T13:00:00Z"},
		{"id":"at-now","expires_at":"2026-09-24T14:00:00Z"},
		{"id":"valid","expires_at":"2026-09-25T14:00:00Z"}]}`)
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "codex-token"},
		CodexResetCreditsURL: server.URL, Now: func() time.Time { return bankedNow }}
	got, err := client.BankedResets(t.Context(), config.Provider{Provider: "codex"})
	want := bankedNow.Add(24 * time.Hour)
	if err != nil || got == nil || len(got.Details) != 1 || got.Details[0].ID != "valid" || got.NextExpiresAt == nil || !got.NextExpiresAt.Equal(want) {
		t.Fatalf("resets=%+v err=%v", got, err)
	}
}

// Anthropic only reports banked resets when asked for the cedar_ember block by
// a Claude Code CLI user agent; anything else is answered "surface" ineligible.
func TestClaudeNativeAsksForBankedResetsAsTheCLI(t *testing.T) {
	server, seen := claudeServer(t, `{`+claudeWindows+`}`)
	if _, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := seen.URL.Query().Get("cedar_ember"); got != "1" {
		t.Fatalf("cedar_ember=%q, want 1 (query %q)", got, seen.URL.RawQuery)
	}
	if ua := seen.Header.Get("User-Agent"); ua != "claude-cli/2.1.280 (external, cli)" {
		t.Fatalf("user agent = %q", ua)
	}
}

func TestClaudeNativeReportsBankedResetsWithSoonestExpiry(t *testing.T) {
	server, _ := claudeServer(t, `{`+claudeWindows+`,"cedar_ember":{"eligible":true,"at_limit":false,
		"grants":[
			{"id":"g-late","label":"Launch","resets_total":1,"resets_left":1,"ends_at":"2026-11-01T00:00:00Z"},
			{"id":"g-soon","label":"Promo","resets_total":2,"resets_left":1,"ends_at":"2026-10-22T23:59:59Z"},
			{"id":"g-spent","label":"Old","resets_total":1,"resets_left":0,"ends_at":"2026-10-01T00:00:00Z"},
			{"id":"g-expired","label":"Gone","resets_total":1,"resets_left":1,"ends_at":"2026-09-20T00:00:00Z"}
		],"next_grant_id":"g-soon"}}`)
	got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil || *got.BankedResets != 2 {
		t.Fatalf("banked resets = %v, want 2 (spent and expired grants excluded)", got.BankedResets)
	}
	want := time.Date(2026, 10, 22, 23, 59, 59, 0, time.UTC)
	if got.BankedResetsExpireAt == nil || !got.BankedResetsExpireAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got.BankedResetsExpireAt, want)
	}
}

func TestClaudeNativeBankedResetsAbsentUnlessEligible(t *testing.T) {
	for name, block := range map[string]string{
		"no block":         ``,
		"ineligible":       `,"cedar_ember":{"eligible":false,"ineligible_reason":"surface","grants":[]}`,
		"malformed grants": `,"cedar_ember":{"eligible":true,"grants":[{"id":"g","ends_at":"2026-10-22T00:00:00Z"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := claudeServer(t, `{`+claudeWindows+block+`}`)
			got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			if got.BankedResets != nil || got.BankedResetsExpireAt != nil {
				t.Fatalf("resets = %v expiry = %v, want absent", got.BankedResets, got.BankedResetsExpireAt)
			}
		})
	}
}

func TestClaudeNativeEligibleWithNoGrantsIsZero(t *testing.T) {
	server, _ := claudeServer(t, `{`+claudeWindows+`,"cedar_ember":{"eligible":true,"grants":[]}}`)
	got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil || *got.BankedResets != 0 || got.BankedResetsExpireAt != nil {
		t.Fatalf("resets = %v expiry = %v, want 0 and no expiry", got.BankedResets, got.BankedResetsExpireAt)
	}
}

func TestClaudeBankedResetsLookupReadsOnlyTheResetBlock(t *testing.T) {
	// No usage windows at all: the supplement must not depend on them.
	server, _ := claudeServer(t, `{"cedar_ember":{"eligible":true,"grants":[{"id":"g","resets_left":1,"ends_at":"2026-10-22T00:00:00Z"}]}}`)
	got, err := claudeClient(server).BankedResets(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Available != 1 || got.NextExpiresAt == nil {
		t.Fatalf("resets = %#v", got)
	}
}

func TestNativeUsageErrorsCarryRetryAfter(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		want   time.Duration
	}{
		"seconds":   {"988", 988 * time.Second},
		"http date": {bankedNow.Add(time.Hour).Format(http.TimeFormat), time.Hour},
		"zero":      {"0", 0},
		"past date": {bankedNow.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			_, err := claudeClient(server).BankedResets(context.Background(), config.Provider{Provider: "claude"})
			var httpErr *nativeusage.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != 429 || httpErr.RetryAfter() != tc.want {
				t.Fatalf("err = %#v, want retry-after %v", err, tc.want)
			}
		})
	}
}

// Real shape of the grant now being rolled out, as published by OpenUsage's
// maintainer from a live response (robinebers/openusage#1291).
func TestClaudeNativeParsesTheOpus55LaunchGrant(t *testing.T) {
	server, _ := claudeServer(t, `{`+claudeWindows+`,"cedar_ember":{
		"eligible":true,"ineligible_reason":null,"at_limit":false,"exhausted":[],
		"grants":[{"id":"opus55-launch-promax-20260921","label":"Claude Opus 5.5 launch: one usage-limit reset for Pro and Max",
			"resets_total":1,"resets_left":1,"starts_at":"2026-09-22T16:00:00+00:00","ends_at":"2026-10-22T16:00:00+00:00",
			"clears":["five_hour","seven_day","seven_day_overage_included"],"paused":false,"usable_now":true,"use_requires_limit":false,
			"percent_used":{"five_hour":0,"seven_day":0,"seven_day_overage_included":0},"blocking":[]}],
		"next_grant_id":"opus55-launch-promax-20260921","weekly_resets_at":"2026-09-29T20:00:00+00:00","cooldown_until":null}}`)
	got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC)
	if got.BankedResets == nil || *got.BankedResets != 1 || got.BankedResetsExpireAt == nil || !got.BankedResetsExpireAt.Equal(want) {
		t.Fatalf("resets=%v expiry=%v", got.BankedResets, got.BankedResetsExpireAt)
	}
}

// The reset lookup is supplementary and must never refresh (and so rotate)
// Claude Code's shared refresh token.
func TestBankedResetLookupUsesReadOnlyCredentials(t *testing.T) {
	server, _ := claudeServer(t, `{"cedar_ember":{"eligible":true,"grants":[]}}`)
	creds := &refreshTrackingCredentials{}
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: creds, ClaudeUsageURL: server.URL, Now: func() time.Time { return bankedNow }}
	if _, err := client.BankedResets(context.Background(), config.Provider{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if creds.refreshing != 0 || creds.readOnly != 1 {
		t.Fatalf("refreshing=%d readOnly=%d", creds.refreshing, creds.readOnly)
	}
}

type refreshTrackingCredentials struct{ refreshing, readOnly int }

func (c *refreshTrackingCredentials) Access(context.Context, string) (nativeusage.Credential, error) {
	c.refreshing++
	return nativeusage.Credential{AccessToken: "t"}, nil
}

func (c *refreshTrackingCredentials) AccessWithoutRefresh(context.Context, string) (nativeusage.Credential, error) {
	c.readOnly++
	return nativeusage.Credential{AccessToken: "t"}, nil
}

func TestClaudeNativeResetDetails(t *testing.T) {
	server, _ := claudeServer(t, `{`+claudeWindows+`,"cedar_ember":{"eligible":true,"next_grant_id":"g","cooldown_until":"2026-09-24T15:00:00Z","grants":[
		{"id":"g","resets_left":2,"ends_at":"2026-10-22T00:00:00Z","clears":["five_hour","seven_day","seven_day_overage_included"],"usable_now":false,"use_requires_limit":true},
		{"id":"other","resets_left":1}, {"id":"spent","resets_left":0}, {"id":"expired","resets_left":1,"ends_at":"2026-09-01T00:00:00Z"}
	]}}`)
	got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BankedResetDetails) != 2 {
		t.Fatalf("details = %#v", got.BankedResetDetails)
	}
	d := got.BankedResetDetails[0]
	if d.ID != "g" || d.ResetsLeft != 2 || !d.Claimable || d.ExpiresAt == nil || d.CooldownUntil == nil || d.UsableNow == nil || *d.UsableNow || d.RequiresLimit == nil || !*d.RequiresLimit || len(d.Clears) != 2 || d.Clears[1] != "weekly" || got.BankedResetDetails[1].Claimable {
		t.Fatalf("details = %#v", got.BankedResetDetails)
	}
}

func TestClaudeNativeMalformedResetDetailsDoNotBreakUsage(t *testing.T) {
	for name, fields := range map[string]struct{ status, grant string }{
		"cooldown_until":     {status: `,"cooldown_until":"unexpected"`},
		"next_grant_id":      {status: `,"next_grant_id":5`},
		"clears":             {grant: `,"clears":"x"`},
		"partial clears":     {grant: `,"clears":["weekly",5]`},
		"usable_now":         {grant: `,"usable_now":"yes"`},
		"use_requires_limit": {grant: `,"use_requires_limit":"yes"`},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := claudeServer(t, `{`+claudeWindows+`,"cedar_ember":{"eligible":true`+fields.status+`,"grants":[{"id":"g","resets_left":2,"ends_at":"2026-10-22T00:00:00Z"`+fields.grant+`}]}}`)
			got, _, err := claudeClient(server).Fetch(context.Background(), config.Provider{Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Short == nil || got.Short.Remaining != .9 || got.Weekly.Remaining != .5 || len(got.Allowances) != 2 || got.BankedResets == nil || *got.BankedResets != 2 || got.BankedResetsExpireAt == nil {
				t.Fatalf("usage or aggregate lost: %#v", got)
			}
			if len(got.BankedResetDetails) != 1 {
				t.Fatalf("details = %#v", got.BankedResetDetails)
			}
			d := got.BankedResetDetails[0]
			if d.CooldownUntil != nil || d.Claimable || len(d.Clears) != 0 || d.UsableNow != nil || d.RequiresLimit != nil {
				t.Fatalf("malformed fields must be unknown: %#v", d)
			}
		})
	}
}

func TestCodexBankedResetDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wham/rate-limit-reset-credits" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer t" || r.Header.Get("ChatGPT-Account-Id") != "account" || r.Header.Get("User-Agent") != "Redline" {
			t.Errorf("request = %#v", r)
		}
		_, _ = w.Write([]byte(`{"available_count":7,"unknown":true,"credits":[{"id":"soon","expires_at":"2026-10-01T00:00:00Z"},{"id":"forever","expires_at":null},{"id":"bad","expires_at":"oops"},{"id":3},{"expires_at":null},null]}`))
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "t", account: "account"}, CodexResetCreditsURL: server.URL + "/wham/rate-limit-reset-credits", Now: func() time.Time { return bankedNow }}
	got, err := client.BankedResets(context.Background(), config.Provider{Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Available != 7 || len(got.Details) != 2 || got.NextExpiresAt == nil {
		t.Fatalf("resets = %#v", got)
	}
	if got.Details[0].ID != "soon" || got.Details[0].ResetsLeft != 1 || len(got.Details[0].Clears) != 2 || got.Details[1].ExpiresAt != nil {
		t.Fatalf("details = %#v", got.Details)
	}
}

func TestCodexNativeReadsResetCreditCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":11,"limit_window_seconds":604800,"reset_at":1790436725}},
			"rate_limit_reset_credits":{"available_count":2,"applicable_available_count":0}}`))
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "t"},
		CodexUsageURL: server.URL, Now: func() time.Time { return bankedNow }}
	got, _, err := client.Fetch(context.Background(), config.Provider{Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.BankedResets == nil || *got.BankedResets != 2 || got.BankedResetsExpireAt != nil {
		t.Fatalf("resets = %v expiry = %v", got.BankedResets, got.BankedResetsExpireAt)
	}
}
