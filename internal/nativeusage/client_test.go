package nativeusage_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/nativeusage"
)

func TestClaudeNativeSnapshotMatchesProviderWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer claude-token" || r.URL.Path != "/api/oauth/usage" {
			t.Fatalf("request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":0,"resets_at":"2026-07-22T20:00:00Z"},"seven_day":{"utilization":48,"resets_at":"2026-07-24T17:00:00Z"},"limits":[{"kind":"weekly_scoped","percent":88,"resets_at":"2026-07-24T17:00:01Z","scope":{"model":{"display_name":"Fable"}}}]}`))
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "claude-token"}, ClaudeUsageURL: server.URL + "/api/oauth/usage", Now: func() time.Time { return time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC) }}
	got, _, err := client.Fetch(context.Background(), config.Provider{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "native" || got.Short == nil || got.Short.Remaining != 1 || got.Weekly.Remaining != .52 {
		t.Fatalf("snapshot=%#v", got)
	}
	fable, ok := got.Allowance("model:fable:weekly")
	if !ok || fable.Remaining != .12 {
		t.Fatalf("fable=%#v ok=%v", fable, ok)
	}
}

func TestCodexNativeClassifiesWeeklyOnlyPrimaryWindowByDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ChatGPT-Account-Id") != "acct" {
			t.Fatalf("account header=%q", r.Header.Get("ChatGPT-Account-Id"))
		}
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":2,"limit_window_seconds":604800,"reset_at":1785259059},"secondary_window":null}}`))
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "codex-token", account: "acct"}, CodexUsageURL: server.URL, Now: time.Now}
	got, _, err := client.Fetch(context.Background(), config.Provider{Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Short != nil || got.Weekly.Remaining != .98 || got.Weekly.ResetsAt.Unix() != 1785259059 {
		t.Fatalf("snapshot=%#v", got)
	}
}

func TestCodexNativeClassifiesFiveHourAndWeeklyWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":30,"limit_window_seconds":18000,"reset_at":1785250000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1785259059}}}`))
	}))
	defer server.Close()
	client := nativeusage.Client{HTTPClient: server.Client(), Credentials: staticCredentials{token: "token"}, CodexUsageURL: server.URL, Now: time.Now}
	got, _, err := client.Fetch(context.Background(), config.Provider{Provider: "codex"})
	if err != nil || got.Short == nil || got.Short.Remaining != .7 || got.Weekly.Remaining != .6 {
		t.Fatalf("snapshot=%#v err=%v", got, err)
	}
}

type staticCredentials struct{ token, account string }

func (s staticCredentials) Access(context.Context, string) (nativeusage.Credential, error) {
	return nativeusage.Credential{AccessToken: s.token, AccountID: s.account}, nil
}
