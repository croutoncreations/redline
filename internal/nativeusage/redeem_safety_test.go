package nativeusage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/usage"
)

type redemptionRoundTripper func(*http.Request) (*http.Response, error)

func (f redemptionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type redemptionErrorBody struct{ read func([]byte) (int, error) }

func (b redemptionErrorBody) Read(p []byte) (int, error) { return b.read(p) }
func (redemptionErrorBody) Close() error                 { return nil }

func TestRedeemRateLimitSurvivesUnreadableBody(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprintf("stalled=%t", stalled), func(t *testing.T) {
			r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
			r.httpClient = &http.Client{Transport: redemptionRoundTripper(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"3600"}}, Body: redemptionErrorBody{read: func([]byte) (int, error) {
					if stalled {
						<-req.Context().Done()
					}
					return 0, io.ErrUnexpectedEOF
				}}}, nil
			})}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			_, err := r.Redeem(ctx, config.Provider{Provider: "codex"}, RedeemRequest{ResetID: "credit", RequestID: "request"})
			now := time.Now()
			manager := usage.NewManager(nil, nil, func() time.Time { return now })
			manager.RecordNativeError("codex", err)
			if !manager.NativeBlockedUntil("codex").Equal(now.Add(time.Hour)) {
				t.Fatalf("lost lockout: %v (%v)", manager.NativeBlockedUntil("codex"), err)
			}
		})
	}
}

func TestRedeemRejectsInvalidOrganizationBeforePost(t *testing.T) {
	for _, profile := range []string{`{}`, `{"organization":null}`, `{"organization":{"uuid":"../evil"}}`, `{"organization":{"uuid":42}}`} {
		t.Run(profile, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Error("posted with invalid org")
				}
				fmt.Fprint(w, profile)
			}))
			defer server.Close()
			r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
			r.claudeBaseURL = server.URL
			_, err := r.Redeem(t.Context(), config.Provider{Provider: "claude"}, RedeemRequest{ResetID: "grant", RequestID: "request"})
			if err == nil || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestRedeemBoundsTimeoutBodyAndRedirects(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `{"organization":{"uuid":"12345678-1234-1234-1234-123456789abc"}}`)
					return
				}
				// A valid prefix must not hide bytes beyond the response limit.
				body := `{"code":"reset","result":"reset"}`
				fmt.Fprint(w, body+strings.Repeat(" ", (2<<20)-len(body))+"not-json")
			}))
			defer server.Close()
			r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
			r.claudeBaseURL = server.URL
			r.codexConsumeURL = server.URL
			r.httpClient = &http.Client{Transport: redemptionRoundTripper(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				want := 20 * time.Second
				if provider == "claude" {
					want = 25 * time.Second
				}
				if !ok || time.Until(deadline) > want || time.Until(deadline) < want-time.Second {
					t.Errorf("deadline=%v", deadline)
				}
				return server.Client().Transport.RoundTrip(req)
			})}
			got, err := r.Redeem(t.Context(), config.Provider{Provider: provider}, RedeemRequest{ResetID: "grant", RequestID: "request"})
			if err == nil || !strings.Contains(err.Error(), "response too large") || got.Outcome == "redeemed" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	forwarded := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
	r.codexConsumeURL = server.URL
	if _, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, RedeemRequest{ResetID: "credit", RequestID: "request"}); err == nil || forwarded {
		t.Fatalf("redirect followed=%t err=%v", forwarded, err)
	}
}

func TestRedeemCodexDryRunAlwaysAllocatesFreshIDsForUnpreparedRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":"no_credit"}`) }))
	defer server.Close()
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "token"}})
	r.codexConsumeURL = server.URL
	req := RedeemRequest{ResetID: "redline-dryrun-previous", RequestID: "previous-request", DryRun: true}
	a, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.ResetID == req.ResetID || a.RequestID == req.RequestID || a.ResetID == b.ResetID || a.RequestID == b.RequestID {
		t.Fatalf("unprepared dry-run IDs reused: %+v %+v", a, b)
	}
	prepared, _ := r.Prepare(t.Context(), config.Provider{Provider: "codex"}, "real-credit", true)
	c, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, prepared)
	if err != nil || c.ResetID != prepared.ResetID || c.RequestID != prepared.RequestID {
		t.Fatalf("prepared IDs changed after persistence: %+v %+v %v", prepared, c, err)
	}
	prepared.ResetID = "real-credit"
	d, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, prepared)
	if err != nil || d.ResetID == "real-credit" {
		t.Fatalf("dry run sent a real ID: %+v %v", d, err)
	}
}

func TestRedeemDefaultCredentialsNeverRefreshOrRewrite(t *testing.T) {
	now := time.Now().UTC()
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, now.Unix()))) + ".sig"
			original := []byte(fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"refresh"}}`, token))
			if provider == "claude" {
				original = []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"token","refreshToken":"refresh","expiresAt":%d}}`, now.UnixMilli()))
			}
			store := &memorySecretStore{value: append([]byte(nil), original...)}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				t.Error("expired redemption must not refresh or send")
			}))
			defer server.Close()
			credentials := &DefaultCredentials{Now: func() time.Time { return now }, ClaudeStore: store, CodexStore: store, ClaudeRefreshURL: server.URL, CodexRefreshURL: server.URL}
			r := NewRedeemer(credentials)
			r.claudeBaseURL = server.URL
			r.codexConsumeURL = server.URL
			got, err := r.Redeem(t.Context(), config.Provider{Provider: provider}, RedeemRequest{ResetID: "reset", RequestID: "request"})
			if err != nil || got.Outcome != "failed" || calls != 0 || store.swaps != 0 || !bytes.Equal(original, store.value) {
				t.Fatalf("%+v %v calls=%d swaps=%d", got, err, calls, store.swaps)
			}
		})
	}
	store := &memorySecretStore{value: []byte(`{"OPENAI_API_KEY":"sk-test"}`)}
	r := NewRedeemer(&DefaultCredentials{CodexStore: store})
	got, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, RedeemRequest{ResetID: "credit", RequestID: "request"})
	if err != nil || got.Reason != "Codex reset credits require a ChatGPT login" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRedeemCodexReadOnlyLoginAndLocalKey(t *testing.T) {
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "sk-api-key"}})
	got, err := r.Redeem(t.Context(), config.Provider{Provider: "codex"}, RedeemRequest{ResetID: "credit", RequestID: "request"})
	if err != nil || got.Outcome != "failed" || got.Reason != "Codex reset credits require a ChatGPT login" {
		t.Fatalf("%+v %v", got, err)
	}
	r = NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "oauth-token"}})
	req, err := r.Prepare(context.Background(), config.Provider{Provider: "codex"}, "credit", false)
	if err != nil || req.RequestID != "00645207-1b8c-5570-80b4-77547f57de88" {
		t.Fatalf("%+v %v", req, err)
	}
	a, _ := r.Prepare(t.Context(), config.Provider{Provider: "codex"}, "real", true)
	b, _ := r.Prepare(t.Context(), config.Provider{Provider: "codex"}, "real", true)
	if a.ResetID == b.ResetID || a.RequestID == b.RequestID {
		t.Fatalf("dry run IDs not fresh: %+v %+v", a, b)
	}
}
