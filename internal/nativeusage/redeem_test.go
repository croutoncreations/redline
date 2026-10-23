package nativeusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

type redemptionCredentials struct {
	credential Credential
	err        error
}

func (c redemptionCredentials) Access(context.Context, string) (Credential, error) {
	panic("redemption must never refresh")
}
func (c redemptionCredentials) AccessWithoutRefresh(context.Context, string) (Credential, error) {
	return c.credential, c.err
}

func TestRedeemClaudeRequestsAndCachedProfile(t *testing.T) {
	profiles := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("User-Agent") != "claude-cli/2.1.280 (external, cli)" || r.Header.Get("Accept") != "application/json" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.URL.Path == "/api/oauth/profile" {
			profiles++
			if r.Method != "GET" {
				t.Error(r.Method)
			}
			fmt.Fprint(w, `{"organization":{"uuid":"12345678-1234-1234-1234-123456789abc","extra":true},"extra":42}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/organizations/12345678-1234-1234-1234-123456789abc/reset_rate_limits" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %s %v", r.Method, r.URL, r.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || body["program"] != "cedar_ember" || body["grant_id"] != "grant_1" || body["request_id"] != "request-1" {
			t.Errorf("body=%v", body)
		}
		fmt.Fprint(w, `{"result":"reset","cleared":["five_hour","seven_day","seven_day_overage_included","unknown"],"resets_left":0}`)
	}))
	defer server.Close()
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
	r.claudeBaseURL = server.URL
	for i := 0; i < 2; i++ {
		got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude"}, RedeemRequest{ResetID: "grant_1", RequestID: "request-1"})
		if err != nil || got.Outcome != "redeemed" || strings.Join(got.Cleared, ",") != "five_hour,weekly" || got.ResetsLeft == nil || *got.ResetsLeft != 0 {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if profiles != 1 {
		t.Fatalf("profiles=%d", profiles)
	}
}

func TestRedeemProviderMappingsAndDryRun(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		codes := map[string]string{"reset": "redeemed", "unknown": "failed"}
		if provider == "claude" {
			for k, v := range map[string]string{"already_used": "redeemed", "not_limited": "nothing_to_reset", "cooldown": "cooldown", "paused": "cooldown", "low_priority_active": "cooldown", "ineligible": "failed", "no_grant": "gone", "unknown_grant": "gone", "not_next_grant": "gone", "unavailable": "failed", "reset_unconfirmed": "failed", "stamp_indeterminate": "failed"} {
				codes[k] = v
			}
		} else {
			for k, v := range map[string]string{"already_redeemed": "redeemed", "nothing_to_reset": "nothing_to_reset", "no_credit": "gone"} {
				codes[k] = v
			}
		}
		for code, outcome := range codes {
			t.Run(provider+"/"+code, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" {
						fmt.Fprint(w, `{"organization":{"uuid":"12345678-1234-1234-1234-123456789abc"}}`)
						return
					}
					fmt.Fprintf(w, `{"result":%q,"code":%q,"cooldown_until":"2030-01-01T00:00:00Z"}`, code, code)
				}))
				defer server.Close()
				r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token", AccountID: "acct"}})
				r.claudeBaseURL = server.URL
				r.codexConsumeURL = server.URL
				got, err := r.Redeem(context.Background(), config.Provider{Provider: provider}, RedeemRequest{ResetID: "grant", RequestID: "req"})
				if err != nil || got.Outcome != outcome || got.Code != code {
					t.Fatalf("got=%+v err=%v", got, err)
				}
				if outcome == "cooldown" && got.CooldownUntil == nil {
					t.Fatal("missing cooldown")
				}
				dry, err := r.Redeem(t.Context(), config.Provider{Provider: provider}, RedeemRequest{ResetID: "real-reset", RequestID: "request", DryRun: true})
				want := outcome
				if (provider == "claude" && (code == "ineligible" || code == "unknown_grant")) || (provider == "codex" && code == "no_credit") {
					want = "dry_run_ok"
				}
				if err != nil || dry.Outcome != want {
					t.Fatalf("dry-run got=%+v err=%v want=%s", dry, err, want)
				}
			})
		}
		t.Run(provider+"/dry", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `{"organization":{"uuid":"12345678-1234-1234-1234-123456789abc"}}`)
					return
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if provider == "claude" {
					if body["grant_id"] != "redline-dryrun" {
						t.Error(body)
					}
					fmt.Fprint(w, `{"result":"unknown_grant"}`)
				} else {
					if !strings.HasPrefix(body["credit_id"], "redline-dryrun-") || body["redeem_request_id"] == "real-request" {
						t.Error(body)
					}
					fmt.Fprint(w, `{"code":"no_credit"}`)
				}
			}))
			defer server.Close()
			r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
			r.claudeBaseURL = server.URL
			r.codexConsumeURL = server.URL
			got, err := r.Redeem(context.Background(), config.Provider{Provider: provider}, RedeemRequest{ResetID: "real-reset", RequestID: "real-request", DryRun: true})
			if err != nil || got.Outcome != "dry_run_ok" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestRedeemCodexBodyHeadersAndDeterministicKey(t *testing.T) {
	var key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/backend-api/wham/rate-limit-reset-credits/consume" {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		for name, want := range map[string]string{"Authorization": "Bearer token", "ChatGPT-Account-Id": "acct", "OpenAI-Beta": "codex-1", "Content-Type": "application/json", "Accept": "application/json"} {
			if r.Header.Get(name) != want {
				t.Errorf("%s=%s", name, r.Header.Get(name))
			}
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 2 || body["credit_id"] != "credit" || body["redeem_request_id"] != key {
			t.Error(body)
		}
		fmt.Fprint(w, `{"code":"reset"}`)
	}))
	defer server.Close()
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token", AccountID: "acct"}})
	r.codexConsumeURL = server.URL + "/backend-api/wham/rate-limit-reset-credits/consume"
	p := config.Provider{Provider: "codex"}
	a, err := r.Prepare(context.Background(), p, "credit", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Prepare(context.Background(), p, "credit", false)
	key = a.RequestID
	if key != b.RequestID || key != "74dd2e76-06c8-505d-8ba8-ca69e9c44691" {
		t.Fatalf("keys %v %v", a, b)
	}
	if _, err := r.Redeem(context.Background(), p, a); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemValidationAuthAndRateLimit(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(status)
			}))
			defer server.Close()
			r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
			r.claudeBaseURL = server.URL
			got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude"}, RedeemRequest{ResetID: "grant", RequestID: "req"})
			if status == 429 {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.RetryAfter() != 2*time.Minute {
					t.Fatalf("%v", err)
				}
			} else if err != nil || got.Outcome != "failed" || !strings.Contains(got.Reason, "auth") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	r := NewRedeemer(redemptionCredentials{err: fmt.Errorf("token near expiry")})
	got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude"}, RedeemRequest{ResetID: "grant", RequestID: "req"})
	if err != nil || got.Reason != "Claude login needs refreshing; run claude to refresh, then retry or redeem in Claude Code (/limit-reset)" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, req := range []RedeemRequest{{ResetID: "../bad", RequestID: "req"}, {ResetID: "ok", RequestID: "bad/id"}} {
		if _, err := r.Redeem(context.Background(), config.Provider{Provider: "claude"}, req); err == nil {
			t.Fatal("invalid id accepted")
		}
	}
}
