package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// creditsAccountPayload is a synthetic OpenAI API credits account as the desktop
// sends it: $62.40 of a $100 promotional grant left, lapsing in 2d5h, $7.25
// of auto-recharge credit, firesale pacing above a $5 floor.
func creditsAccountPayload(status string, stale bool, reason string) string {
	expires := cursorNow.Add(53 * time.Hour).Format(time.RFC3339)
	effective := cursorNow.Add(-27 * 24 * time.Hour).Format(time.RFC3339)
	paidExpires := cursorNow.Add(300 * 24 * time.Hour).Format(time.RFC3339)
	staleJSON := "false"
	if stale {
		staleJSON = "true"
	}
	return `{"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"` + status + `",
	  "snapshot_stale":` + staleJSON + `,"usage_source":{"active":"apicredits","reason":"` + reason + `"},
	  "latest_decision":{"decision":"RUN","unlocked_tier":"firesale","projected_trigger_at":"2026-10-09T12:00:04Z","reason":"$62.40 of promotional credit expires in 2 days; running queued work before it lapses"},
	  "scheduling":{"rolling_reserve":0.25,"pace_thresholds":[]},
	  "snapshot":{"provider":"openai-api-credits","observed_at":"` + cursorNow.Add(-time.Minute).Format(time.RFC3339) + `",
	   "weekly":{"remaining":0.624,"resets_at":"` + expires + `","period_seconds":2592000},
	   "allowances":[{"key":"weekly","scope":"account","role":"weekly","remaining":0.624,"resets_at":"` + expires + `","period_duration_seconds":2592000}],
	   "credits":{"promo_remaining":62.4,"promo_granted":100,"next_expiring":62.4,
	    "effective_at":"` + effective + `","expires_at":"` + expires + `",
	    "paid_available":7.25,"min_promo_balance":5,"pacing":"firesale","firesale_window_seconds":259200,
	    "grants":[{"kind":"promo","amount":100,"remaining":62.4,"effective_at":"` + effective + `","expires_at":"` + expires + `"},
	     {"kind":"auto_recharge","amount":10,"remaining":7.25,"expires_at":"` + paidExpires + `"}]}}}`
}

func TestCreditsAccountRendersDollarsNotAWeeklyWindow(t *testing.T) {
	view := renderPayload(t, `{"providers":[`+creditsAccountPayload("ok", false, "")+`]}`)
	account := view.Providers[0]
	credits := account.Credits
	if credits == nil {
		t.Fatal("credits view missing")
	}
	if credits.Headline != "$62.40 left" || credits.Detail != "Expires in 2 days" {
		t.Fatalf("headline/detail = %q / %q", credits.Headline, credits.Detail)
	}
	if credits.RemainingPercent != 62 || credits.PromoLabel != "$62.40 of $100.00 left" {
		t.Fatalf("promo = %d %q", credits.RemainingPercent, credits.PromoLabel)
	}
	if !credits.ExpiresAt.Equal(cursorNow.Add(53*time.Hour)) || credits.ExpiresInSeconds != 53*3600 {
		t.Fatalf("expiry = %v %d", credits.ExpiresAt, credits.ExpiresInSeconds)
	}
	if credits.RuleLine != "Jobs start above $5.00 · Firesale: runs within 3 days of expiry" {
		t.Fatalf("rule = %q", credits.RuleLine)
	}
	if credits.PaidLabel != "$7.25 · shown only, never scheduled" {
		t.Fatalf("paid = %q", credits.PaidLabel)
	}
	if len(credits.Grants) != 2 || credits.Grants[0].Label != "ChatGPT subscription grant" ||
		credits.Grants[0].AmountLabel != "$62.40 of $100.00" || credits.Grants[1].Label != "Auto recharge" ||
		credits.Grants[1].AmountLabel != "$7.25 of $10.00" {
		t.Fatalf("grants = %+v", credits.Grants)
	}
	if credits.NoPromo || credits.SignedOut || credits.Stale {
		t.Fatalf("flags = %+v", credits)
	}
	// Never a weekly or monthly window, and no pool meters.
	if account.WeeklyLabel != "" || account.WeeklyNotReported || len(account.Pools) != 0 || account.Session != nil || account.SessionUnknown {
		t.Fatalf("window fields leaked: %+v", account)
	}
	encoded, _ := json.Marshal(account)
	for _, banned := range []string{"Weekly", "Monthly", "5h", "weekly allowance"} {
		if strings.Contains(string(encoded), banned) {
			t.Fatalf("%q in %s", banned, encoded)
		}
	}
	if account.Scheduling == nil || account.Scheduling.Decision != "RUN" || !account.Scheduling.ProjectedTriggerAt.IsZero() {
		t.Fatalf("scheduling = %+v", account.Scheduling)
	}
}

func TestCreditsAccountWithNoPromotionalCredit(t *testing.T) {
	body := `{"id":"api-credits","provider":"openai-api-credits","account_status":"ok",
	  "snapshot":{"provider":"openai-api-credits","observed_at":"` + cursorNow.Add(-time.Minute).Format(time.RFC3339) + `",
	   "weekly_unavailable":true,
	   "credits":{"promo_remaining":0,"promo_granted":0,"next_expiring":0,"paid_available":7.25,"min_promo_balance":5,"pacing":"even",
	    "grants":[{"kind":"purchase","amount":20,"remaining":7.25}]}}}`
	credits := renderPayload(t, `{"providers":[`+body+`]}`).Providers[0].Credits
	if credits == nil || !credits.NoPromo || credits.Headline != "$0.00 promo" || credits.Detail != "No promotional credit" {
		t.Fatalf("credits = %+v", credits)
	}
	if credits.RemainingPercent != 0 || !credits.ExpiresAt.IsZero() || credits.PaidLabel != "$7.25 · shown only, never scheduled" {
		t.Fatalf("credits = %+v", credits)
	}
	if credits.RuleLine != "Jobs start above $5.00 · Even pacing across the grant" {
		t.Fatalf("rule = %q", credits.RuleLine)
	}
	if len(credits.Grants) != 1 || credits.Grants[0].Label != "Purchased credit" || !credits.Grants[0].ExpiresAt.IsZero() {
		t.Fatalf("grants = %+v", credits.Grants)
	}
	if account := renderPayload(t, `{"providers":[`+body+`]}`).Providers[0]; account.WeeklyNotReported {
		t.Fatal("no promo must not read as weekly not reported")
	}
}

func TestCreditsAccountStaleAndSignedOut(t *testing.T) {
	stale := renderPayload(t, `{"providers":[`+creditsAccountPayload("stale", true, "")+`]}`).Providers[0]
	if stale.Credits == nil || !stale.Credits.Stale || stale.Credits.Headline != "Last known $62.40" ||
		stale.Credits.Detail != "Last sample 1 min ago" {
		t.Fatalf("stale = %+v", stale.Credits)
	}
	// The last RUN was about a balance that is no longer current.
	if stale.Credits.DecisionLabel != "" || stale.Credits.DecisionReason != "" {
		t.Fatalf("stale decision = %q / %q", stale.Credits.DecisionLabel, stale.Credits.DecisionReason)
	}
	signedOut := renderPayload(t, `{"providers":[`+creditsAccountPayload("signed_out", true, "signed_out")+`]}`).Providers[0]
	credits := signedOut.Credits
	if credits == nil || !credits.SignedOut || credits.Headline != "Session key needed" ||
		credits.Detail != "Session key expired · re-paste it on the computer running Redline" {
		t.Fatalf("signed out = %+v", credits)
	}
	if signedOut.StaleReason != credits.Detail {
		t.Fatalf("stale reason = %q", signedOut.StaleReason)
	}
	// Signed out with a stale RUN decision and an old balance: only the fix.
	if credits.DecisionLabel != "" || credits.DecisionReason != "" || credits.PromoLabel != "" || credits.RuleLine != "" ||
		credits.PaidLabel != "" || len(credits.Grants) != 0 || credits.RemainingPercent != 0 {
		t.Fatalf("signed out shows more than the fix: %+v", credits)
	}
	// Signed out with no snapshot at all still names the fix.
	bare := renderPayload(t, `{"providers":[{"id":"api-credits","provider":"openai-api-credits","account_status":"signed_out",
	  "usage_source":{"reason":"signed_out"}}]}`).Providers[0]
	if bare.Credits == nil || bare.Credits.Headline != "Session key needed" {
		t.Fatalf("bare = %+v", bare.Credits)
	}
}

func TestCreditsMoneyAndDurationWording(t *testing.T) {
	for amount, want := range map[float64]string{0: "$0.00", 7.255: "$7.26", 1234.5: "$1,234.50", -3: "$0.00", 1000000: "$1,000,000.00"} {
		if got := usd(amount); got != want {
			t.Errorf("usd(%v) = %q, want %q", amount, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		53 * time.Hour: "in 2 days", 30 * time.Hour: "in 1 day", 5 * time.Hour: "in 5 hours", time.Hour: "in 1 hour",
		10 * time.Minute: "in 10 minutes", 30 * time.Second: "in 1 minute",
	} {
		if got := creditsCountdown(d); got != want {
			t.Errorf("creditsCountdown(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestOtherProvidersCarryNoCreditsView(t *testing.T) {
	view := renderPayload(t, `{"providers":[{"id":"claude-main","provider":"claude","snapshot":{"provider":"claude",
	  "weekly":{"remaining":0.5,"resets_at":"2026-10-12T00:00:00Z"}}}]}`)
	if view.Providers[0].Credits != nil || view.Providers[0].WeeklyLabel != "Weekly allowance" {
		t.Fatalf("claude = %+v", view.Providers[0])
	}
}

func TestDisplayProviderNamesCreditsAccounts(t *testing.T) {
	if got := displayProvider("openai-api-credits"); got != "OpenAI API credits" {
		t.Fatalf("displayProvider = %q", got)
	}
}

func TestCreditsDecisionWordingMatchesTheDashboards(t *testing.T) {
	fresh := renderPayload(t, `{"providers":[`+creditsAccountPayload("ok", false, "")+`]}`).Providers[0].Credits
	if fresh.DecisionLabel != "Run now · firesale" ||
		fresh.DecisionReason != "$62.40 of promotional credit expires in 2 days; running queued work before it lapses" {
		t.Fatalf("decision = %q / %q", fresh.DecisionLabel, fresh.DecisionReason)
	}
	for _, c := range []struct{ decision, tier, reason, want string }{
		{"RUN", "", "x", "Run now"},
		{"WAIT", "", "promotional credit is at or below the $5.00 floor", "Held · at the floor"},
		{"WAIT", "", "$2.00 of promotional credit is reserved for 2 run(s) in flight; waiting for one to finish before starting another", "Held · run in flight"},
		{"WAIT", "", "no promotional API credit is available", "Held · no promotional credit"},
		{"WAIT", "", "holding promotional credit until it is within 3 days of expiring", "Holding credit"},
		{"UNKNOWN", "", "usage unavailable", "Dispatch held"},
	} {
		if got := creditsDecisionLabel(c.decision, c.tier, c.reason); got != c.want {
			t.Errorf("creditsDecisionLabel(%q, %q) = %q, want %q", c.decision, c.reason, got, c.want)
		}
	}
	if got := capitalised("no promotional API credit is available"); got != "No promotional API credit is available" {
		t.Errorf("capitalised = %q", got)
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "30 secs ago", time.Minute: "1 min ago", 2 * time.Hour: "2 hrs ago", 26 * time.Hour: "1 day ago"} {
		if got := creditsAgo(d); got != want {
			t.Errorf("creditsAgo(%v) = %q, want %q", d, got, want)
		}
	}
}

// Grants lapse at 00:00 UTC. The view carries the instant in UTC whatever the
// host's zone, so the phone can show the UTC date (Dec 13), never the US
// Pacific one (Dec 12).
func TestCreditsExpiryStaysUTCOnAPacificHost(t *testing.T) {
	pacific, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	host := time.Local
	time.Local = pacific
	t.Cleanup(func() { time.Local = host })

	payload := strings.ReplaceAll(creditsAccountPayload("ok", false, ""), cursorNow.Add(53*time.Hour).Format(time.RFC3339), "2026-12-13T00:00:00Z")
	credits := renderPayload(t, `{"providers":[`+payload+`]}`).Providers[0].Credits
	raw, err := json.Marshal(credits)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expires_at":"2026-12-13T00:00:00Z"`) || credits.ExpiresAt.Location() != time.UTC {
		t.Fatalf("expires_at = %v in %s", credits.ExpiresAt, raw)
	}
	if len(credits.Grants) == 0 || credits.Grants[0].ExpiresAt.Location() != time.UTC || credits.Grants[0].ExpiresAt.Day() != 13 {
		t.Fatalf("grant expiry = %+v", credits.Grants)
	}
}
