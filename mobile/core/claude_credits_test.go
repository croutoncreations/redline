package core

import (
	"strings"
	"testing"
)

// claudeCreditsAccountPayload is a synthetic Claude (Anthropic) API credits
// account: the OpenAI payload's shape with the Claude provider id and
// Console tranche kinds.
func claudeCreditsAccountPayload(status string, stale bool, reason string) string {
	body := creditsAccountPayload(status, stale, reason)
	body = strings.ReplaceAll(body, `"openai-api-credits"`, `"anthropic-api-credits"`)
	body = strings.Replace(body, `"id":"api-credits"`, `"id":"claude-credits"`, 1)
	body = strings.Replace(body, `"label":"Linked org"`, `"label":"Console org"`, 1)
	return strings.Replace(body, `{"kind":"auto_recharge"`, `{"kind":"other"`, 1)
}

func TestClaudeCreditsAccountRendersLikeOpenAICredits(t *testing.T) {
	account := renderPayload(t, `{"providers":[`+claudeCreditsAccountPayload("ok", false, "")+`]}`).Providers[0]
	credits := account.Credits
	if credits == nil || credits.Headline != "$62.40 left" || credits.PromoLabel != "$62.40 of $100.00 left" {
		t.Fatalf("credits = %+v", credits)
	}
	if len(credits.Grants) != 2 || credits.Grants[0].Label != "Claude subscription credit" || credits.Grants[1].Label != "Other promotional credit" {
		t.Fatalf("grants = %+v", credits.Grants)
	}
	if account.WeeklyNotReported {
		t.Fatal("a Claude credits account read as an unreported weekly limit")
	}
}

func TestClaudeCreditsSignedOutNamesTheConsole(t *testing.T) {
	account := renderPayload(t, `{"providers":[`+claudeCreditsAccountPayload("signed_out", true, "signed_out")+`]}`).Providers[0]
	want := "Session key expired · re-paste it from platform.claude.com on the computer running Redline"
	if account.Credits == nil || account.Credits.Detail != want || account.StaleReason != want {
		t.Fatalf("credits = %+v stale = %q", account.Credits, account.StaleReason)
	}
	openai := renderPayload(t, `{"providers":[`+creditsAccountPayload("signed_out", true, "signed_out")+`]}`).Providers[0]
	if openai.Credits.Detail != creditsSignedOutLine {
		t.Fatalf("openai detail = %q", openai.Credits.Detail)
	}
}

func TestDisplayProviderNamesClaudeCreditsAccounts(t *testing.T) {
	if got := displayProvider("anthropic-api-credits"); got != "Claude API credits" {
		t.Fatalf("display = %q", got)
	}
}
