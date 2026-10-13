package core_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/mobile/core"
)

// Two Claude accounts and one Codex account, synthetic. Each Claude account
// keeps its own plan, numbers, reset credits and state; nothing is combined.
const multiAccountPayload = `{
	"generated_at": "2026-07-20T19:00:00Z",
	"providers": [
		{"id": "claude-client", "provider": "claude", "label": "Client", "openusage_card": "claude@ab12cd34",
		 "account_status": "stale", "snapshot_stale": true, "usage_source": {"active": "openusage"},
		 "snapshot": {"provider": "claude", "account": "claude-client", "observed_at": "2026-07-20T18:30:00Z",
		   "plan": "Team", "source_stale": true, "source": "openusage",
		   "weekly": {"remaining": 0.58, "resets_at": "2026-07-23T03:00:00Z"}}},
		{"id": "claude-personal", "provider": "claude", "label": "Personal", "openusage_card": "claude",
		 "account_status": "ok", "usage_source": {"active": "openusage"},
		 "snapshot": {"provider": "claude", "account": "claude-personal", "observed_at": "2026-07-20T18:59:00Z",
		   "plan": "Max 20x", "source": "openusage",
		   "short": {"remaining": 0.64, "resets_at": "2026-07-20T22:00:00Z"},
		   "weekly": {"remaining": 0.41, "resets_at": "2026-07-24T16:00:00Z"},
		   "banked_resets": 3, "banked_resets_expire_at": "2026-07-22T00:00:00Z",
		   "banked_resets_expiries": ["2026-07-19T00:00:00Z", "2026-07-30T00:00:00Z", "2026-07-22T00:00:00Z"]}},
		{"id": "codex-main", "provider": "codex", "account_status": "ok",
		 "snapshot": {"provider": "codex", "observed_at": "2026-07-20T18:59:00Z", "source": "openusage",
		   "weekly": {"remaining": 0.9, "resets_at": "2026-07-25T00:00:00Z"}}}
	]
}`

func TestAccountsAreNumberedPerProviderAndKeepTheirOwnData(t *testing.T) {
	server := usagePayload(t, multiAccountPayload)
	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	view := decodeUsage(t, raw)
	if len(view.Providers) != 3 {
		t.Fatalf("providers = %d", len(view.Providers))
	}
	client0, personal, codex := view.Providers[0], view.Providers[1], view.Providers[2]
	if client0.AccountIndex != 1 || client0.AccountCount != 2 || personal.AccountIndex != 2 || personal.AccountCount != 2 {
		t.Fatalf("claude numbering = %d/%d %d/%d", client0.AccountIndex, client0.AccountCount, personal.AccountIndex, personal.AccountCount)
	}
	if codex.AccountIndex != 1 || codex.AccountCount != 1 || codex.Label != "codex-main" {
		t.Fatalf("codex = %+v", codex)
	}
	if client0.Label != "Client" || client0.Plan != "Team" || client0.AccountStatus != "stale" || !client0.Stale ||
		client0.StaleReason == "" || client0.ResetCredits != nil || client0.BankedResets != nil {
		t.Fatalf("client = %+v", client0)
	}
	if personal.Plan != "Max 20x" || personal.Weekly.RemainingPercent != 41 || client0.Weekly.RemainingPercent != 58 {
		t.Fatalf("weekly numbers crossed accounts: personal %d client %d", personal.Weekly.RemainingPercent, client0.Weekly.RemainingPercent)
	}
	credits := personal.ResetCredits
	if credits == nil || credits.Available != 3 || len(credits.Expiries) != 2 {
		t.Fatalf("credits = %+v", credits)
	}
	if !credits.Expiries[0].Equal(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)) ||
		credits.NextExpiresInSeconds != int64(29*time.Hour/time.Second) {
		t.Fatalf("credit expiries = %v next %d", credits.Expiries, credits.NextExpiresInSeconds)
	}
	// Credit deadlines are separate from the allowance reset.
	if credits.Expiries[0].Equal(personal.Weekly.ResetsAt) {
		t.Fatal("credit deadline must not be the weekly reset")
	}
}

func TestOlderDesktopWithoutAccountFieldsStillRenders(t *testing.T) {
	server := usagePayload(t, `{"generated_at":"2026-07-20T19:00:00Z","providers":[
		{"id":"claude-main","provider":"claude","snapshot":{"provider":"claude","observed_at":"2026-07-20T18:59:00Z","source":"native",
		 "weekly":{"remaining":0.5,"resets_at":"2026-07-24T00:00:00Z"},"banked_resets":1}}]}`)
	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	provider := decodeUsage(t, raw).Providers[0]
	if provider.Label != "claude-main" || provider.AccountStatus != "unknown" || provider.AccountCount != 1 ||
		provider.ResetCredits == nil || provider.ResetCredits.Available != 1 || len(provider.ResetCredits.Expiries) != 0 {
		t.Fatalf("provider = %+v credits %+v", provider, provider.ResetCredits)
	}
}

func TestAccountsScreenWordsStateAndSignIn(t *testing.T) {
	var method, path, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"restart_required":true,"accounts":[
		 {"id":"claude-client","provider":"claude","label":"Client","openusage_card":"claude@ab12cd34","origin":"managed","pending":"change","sign_in":"Sign in in OpenUsage."},
		 {"id":"claude-main","provider":"claude","origin":"config","running":true,"status":"signed_out","sign_in":"Run claude auth login."}]}`))
	}))
	defer server.Close()
	client := core.NewClientWithClock(server.URL, "test-token", func() time.Time { return fixedNow })
	raw, err := client.RenameAccount("claude-client", "  Northwind ")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch || path != "/v1/accounts/claude-client" || body != `{"label":"Northwind"}` {
		t.Fatalf("%s %s %s", method, path, body)
	}
	var view core.AccountsView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	if !view.RestartRequired || len(view.Accounts) != 2 {
		t.Fatalf("view = %+v", view)
	}
	clientAccount, main := view.Accounts[0], view.Accounts[1]
	if !clientAccount.Editable || clientAccount.Tone != "warn" || !strings.Contains(clientAccount.State, "restart") ||
		clientAccount.Reads != "OpenUsage card claude@ab12cd34" {
		t.Fatalf("client = %+v", clientAccount)
	}
	if main.Editable || main.State != "Signed out" || main.Tone != "danger" || main.Label != "claude-main" ||
		main.SignIn != "Run claude auth login." {
		t.Fatalf("main = %+v", main)
	}
	if _, err := client.AddAccount("x", "claude", "X", ""); err == nil {
		t.Fatal("an account without a card cannot be added from the phone")
	}
	if _, err := client.DiscoverAccounts("../x"); err == nil {
		t.Fatal("unsupported provider accepted")
	}
}
