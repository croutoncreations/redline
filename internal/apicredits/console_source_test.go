package apicredits

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func anthropicCreditsProvider(org string) config.Provider {
	return config.Provider{Provider: config.ProviderAnthropicAPICredits, Label: "Claude credits",
		APICredits: &config.APICredits{OrganizationID: org, MinPromoBalance: new(float64)}}
}

func TestUsageSourceReadsConsoleCreditsForAnthropicAccounts(t *testing.T) {
	server, seen := fakeConsole(t, http.StatusOK, string(loadConsoleFixture(t, "console-prepaid-credits.json")))
	secrets := &memorySecrets{}
	_ = secrets.Replace(context.Background(), "claude-credits", SecretSessionKey, fakeConsoleSessionKey)
	source := UsageSource{Next: &nextSource{}, Console: testConsoleClient(server), Secrets: secrets}
	ctx := WithAccount(context.Background(), "claude-credits")
	snapshot, raw, err := source.Fetch(ctx, anthropicCreditsProvider(fakeConsoleOrg))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != config.ProviderAnthropicAPICredits || snapshot.Credits == nil ||
		snapshot.Credits.PromoRemaining != 62.4 || snapshot.Credits.PaidAvailable != 25.25 {
		t.Fatalf("snapshot = %+v credits = %+v", snapshot, snapshot.Credits)
	}
	if got := snapshot.Weekly.Remaining; got < 0.6239 || got > 0.6241 {
		t.Fatalf("remaining = %v", got)
	}
	for _, leaked := range []string{"sk-ant", "tranche_SYNTHETIC", "promo_SYNTHETIC", fakeConsoleOrg} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("raw payload contains %q", leaked)
		}
	}
	if len(*seen) != 1 || (*seen)[0].Header.Get("Cookie") != "sessionKey="+fakeConsoleSessionKey {
		t.Fatalf("requests = %d", len(*seen))
	}
}

func TestUsageSourceReportsConsoleSessionProblemsAsSignedOut(t *testing.T) {
	server, _ := fakeConsole(t, http.StatusForbidden, `{"type":"error"}`)
	secrets := &memorySecrets{}
	source := UsageSource{Next: &nextSource{}, Console: testConsoleClient(server), Secrets: secrets}
	ctx := WithAccount(context.Background(), "claude-credits")
	for _, step := range []struct {
		name  string
		setup func()
	}{
		{"missing", func() {}},
		{"wrong key", func() { _ = secrets.Replace(ctx, "claude-credits", SecretSessionKey, "sess-OPENAIKEY") }},
		{"rejected", func() { _ = secrets.Replace(ctx, "claude-credits", SecretSessionKey, fakeConsoleSessionKey) }},
	} {
		name := step.name
		step.setup()
		_, _, err := source.Fetch(ctx, anthropicCreditsProvider(fakeConsoleOrg))
		var reasoned interface{ Reason() string }
		if !errors.As(err, &reasoned) || reasoned.Reason() != "signed_out" {
			t.Fatalf("%s: err = %v", name, err)
		}
		if strings.Contains(err.Error(), "FAKEKEYFORTESTS") || strings.Contains(err.Error(), "OPENAIKEY") {
			t.Fatalf("%s: error leaks the key", name)
		}
		if name == "missing" && (!strings.Contains(err.Error(), "Claude Console") || !strings.Contains(err.Error(), "set-session-key claude-credits")) {
			t.Fatalf("missing: err = %v", err)
		}
	}
}

// A missing organization is a configuration problem, not a sign-in one; a
// rate limit backs off.
func TestUsageSourceConsoleNotFoundAndRateLimit(t *testing.T) {
	secrets := &memorySecrets{}
	_ = secrets.Replace(context.Background(), "claude-credits", SecretSessionKey, fakeConsoleSessionKey)
	ctx := WithAccount(context.Background(), "claude-credits")
	notFound, _ := fakeConsole(t, http.StatusNotFound, `{}`)
	_, _, err := UsageSource{Next: &nextSource{}, Console: testConsoleClient(notFound), Secrets: secrets}.Fetch(ctx, anthropicCreditsProvider(fakeConsoleOrg))
	var reasoned interface{ Reason() string }
	if !errors.Is(err, ErrConsoleOrganizationNotFound) || errors.As(err, &reasoned) {
		t.Fatalf("404: err = %v", err)
	}
	limited, _ := fakeConsole(t, http.StatusTooManyRequests, `{}`)
	_, _, err = UsageSource{Next: &nextSource{}, Console: testConsoleClient(limited), Secrets: secrets}.Fetch(ctx, anthropicCreditsProvider(fakeConsoleOrg))
	var throttled interface{ RateLimited() bool }
	if !errors.As(err, &throttled) || !throttled.RateLimited() {
		t.Fatalf("429: err = %v", err)
	}
}

// REDLINE_OPENAI_SESSION_KEY is an OpenAI platform key: it never stands in
// for a Claude account's missing Console key.
func TestOpenAISessionKeyEnvironmentNeverServesAnthropicAccounts(t *testing.T) {
	t.Setenv(SessionKeyEnv, "sess-FROMENV")
	CaptureSessionKeyEnv()
	t.Cleanup(func() { capturedSessionKey.Store("") })
	store := FileSecretStore{Dir: t.TempDir()}
	if _, err := ResolveSecret(context.Background(), store, config.APICreditsVendorAnthropic, "acct", SecretSessionKey, ""); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("anthropic err = %v", err)
	}
	if got, err := ResolveSecret(context.Background(), store, config.APICreditsVendorOpenAI, "acct", SecretSessionKey, ""); err != nil || got != "sess-FROMENV" {
		t.Fatalf("openai = %q, %v", got, err)
	}
}
