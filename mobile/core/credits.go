package core

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
)

// creditsProvider is the provider name of an OpenAI API credits account: a
// dollar balance, not a subscription window.
const creditsProvider = "openai-api-credits"

// creditsSignedOutLine is the phone's fix for an expired session key. Keys
// are only ever entered on the computer running Redline, never on a phone.
const creditsSignedOutLine = "Session key expired · re-paste it on the computer running Redline"

// defaultCreditsFiresaleWindow matches the scheduler's default when a
// snapshot does not carry the account's own window.
const defaultCreditsFiresaleWindow = 72 * time.Hour

// CreditsView is an OpenAI API credits account, worded for display. Only
// promotional credit is capacity; paid credit is shown and never scheduled.
type CreditsView struct {
	// Headline is "$62.40 left", "$0.00 promo", "Last known $62.40" or
	// "Session key needed".
	Headline string `json:"headline"`
	// Detail is the line under the headline: "Expires in 2 days", "No
	// promotional credit", "Expired", or the signed-out fix. The screen adds
	// the local date of ExpiresAt, which depends on the phone's timezone.
	Detail string `json:"detail"`
	// ExpiresAt is the soonest promotional grant's expiry; zero when no
	// promotional credit is left.
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	ExpiresInSeconds int64     `json:"expires_in_seconds,omitempty"`
	// RemainingPercent is promotional credit left of what was granted, 0-100.
	RemainingPercent int `json:"remaining_percent"`
	// PromoLabel is "$62.40 of $100.00 left".
	PromoLabel string `json:"promo_label"`
	// RuleLine says when jobs start: the floor and the pacing.
	RuleLine string `json:"rule_line"`
	// PaidLabel is "$7.25 · shown only, never scheduled", or "" when there is
	// no paid credit and the row is omitted.
	PaidLabel string            `json:"paid_label,omitempty"`
	Grants    []CreditGrantView `json:"grants,omitempty"`
	// DecisionLabel is the scheduler's decision in the dashboards' words:
	// "Run now · firesale", "Held · at the floor", …; DecisionReason is its
	// reason, first letter capitalised. Both empty when signed out or stale,
	// where the last decision was about a balance that is no longer current.
	DecisionLabel  string `json:"decision_label,omitempty"`
	DecisionReason string `json:"decision_reason,omitempty"`
	NoPromo        bool   `json:"no_promo,omitempty"`
	Stale          bool   `json:"stale,omitempty"`
	SignedOut      bool   `json:"signed_out,omitempty"`
}

// CreditGrantView is one grant with money left.
type CreditGrantView struct {
	// Label names the grant's kind: "ChatGPT subscription grant", "Auto
	// recharge", "Purchased credit" or "Credit grant".
	Label string `json:"label"`
	// AmountLabel is "$62.40 of $100.00".
	AmountLabel string `json:"amount_label"`
	// ExpiresAt is zero when the grant reported no expiry.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

func isCreditsProvider(provider string) bool {
	return strings.EqualFold(provider, creditsProvider)
}

// creditsView words a credits account's balance. Nil when there is neither a
// balance nor a signed-out session to explain.
func creditsView(snapshot *decision.UsageSnapshot, stale, signedOut bool, now time.Time) *CreditsView {
	var balance *decision.CreditsBalance
	if snapshot != nil {
		balance = snapshot.Credits
	}
	if balance == nil && !signedOut {
		return nil
	}
	view := &CreditsView{Stale: stale, SignedOut: signedOut}
	if balance != nil {
		fillCredits(view, balance, snapshot.WeeklyUnavailable, now)
	}
	switch {
	case signedOut:
		// Only the fix: the old balance, rule, paid credit and grants are not
		// shown beside "Session key needed".
		*view = CreditsView{SignedOut: true, Headline: "Session key needed", Detail: creditsSignedOutLine}
	case stale:
		view.Headline = "Last known " + usd(balance.PromoRemaining)
		view.Detail = "Last sample " + creditsAgo(now.Sub(snapshot.ObservedAt))
	}
	return view
}

// creditsDecisionLabel words a credits decision as every dashboard does.
func creditsDecisionLabel(decision, tier, reason string) string {
	switch decision {
	case "RUN", "ADMIT":
		if tier == "firesale" {
			return "Run now · firesale"
		}
		return "Run now"
	case "UNKNOWN":
		return "Dispatch held"
	}
	switch {
	case strings.Contains(reason, "floor"):
		return "Held · at the floor"
	case strings.Contains(reason, "no promotional"):
		return "Held · no promotional credit"
	default:
		return "Holding credit"
	}
}

// capitalised upper-cases a sentence's first letter, adding no full stop.
func capitalised(text string) string {
	if text == "" {
		return ""
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// creditsAgo is "2 hrs ago", "5 mins ago" or "1 day ago", as the web
// dashboards word a sample's age.
func creditsAgo(d time.Duration) string {
	seconds := max(int(d.Seconds()), 0)
	amount, unit := seconds, "sec"
	switch {
	case seconds >= 86400:
		amount, unit = int(math.Round(float64(seconds)/86400)), "day"
	case seconds >= 3600:
		amount, unit = int(math.Round(float64(seconds)/3600)), "hr"
	case seconds >= 60:
		amount, unit = int(math.Round(float64(seconds)/60)), "min"
	}
	if amount != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s ago", amount, unit)
}

func fillCredits(view *CreditsView, balance *decision.CreditsBalance, weeklyUnavailable bool, now time.Time) {
	view.NoPromo = weeklyUnavailable || balance.PromoRemaining <= 0
	if balance.PromoGranted > 0 && !view.NoPromo {
		view.RemainingPercent = percent(balance.PromoRemaining / balance.PromoGranted)
	}
	view.PromoLabel = usd(balance.PromoRemaining) + " of " + usd(balance.PromoGranted) + " left"
	switch {
	case view.NoPromo:
		view.Headline = "$0.00 promo"
		view.Detail = "No promotional credit"
	default:
		view.Headline = usd(balance.PromoRemaining) + " left"
		if balance.ExpiresAt != nil {
			view.ExpiresAt = balance.ExpiresAt.UTC()
			view.ExpiresInSeconds = secondsUntil(*balance.ExpiresAt, now)
			if balance.ExpiresAt.After(now) {
				view.Detail = "Expires " + creditsCountdown(balance.ExpiresAt.Sub(now))
			} else {
				view.Detail = "Expired"
			}
		}
	}
	view.RuleLine = "Jobs start above " + usd(balance.MinPromoBalance) + " · "
	if balance.Pacing == "even" {
		view.RuleLine += "Even pacing across the grant"
	} else {
		window := time.Duration(balance.FiresaleWindowSeconds) * time.Second
		if window <= 0 {
			window = defaultCreditsFiresaleWindow
		}
		view.RuleLine += "Firesale: runs within " + creditsSpan(window) + " of expiry"
	}
	if balance.PaidAvailable > 0 {
		view.PaidLabel = usd(balance.PaidAvailable) + " · shown only, never scheduled"
	}
	for _, grant := range balance.Grants {
		if grant.Remaining <= 0 {
			continue
		}
		item := CreditGrantView{Label: creditGrantLabel(grant.Kind), AmountLabel: usd(grant.Remaining) + " of " + usd(grant.Amount)}
		if grant.ExpiresAt != nil {
			item.ExpiresAt = grant.ExpiresAt.UTC()
		}
		view.Grants = append(view.Grants, item)
	}
}

func creditGrantLabel(kind string) string {
	switch kind {
	case "promo":
		return "ChatGPT subscription grant"
	case "auto_recharge":
		return "Auto recharge"
	case "purchase":
		return "Purchased credit"
	default:
		return "Credit grant"
	}
}

// usd formats a dollar amount with two decimals and thousands separators.
// Negative amounts, which no balance can be, read as zero.
func usd(amount float64) string {
	cents := int64(math.Round(math.Max(0, amount) * 100))
	whole := fmt.Sprintf("%d", cents/100)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	return fmt.Sprintf("$%s.%02d", whole, cents%100)
}

// creditsCountdown is "in 2 days", "in 5 hours" or "in 10 minutes": whole
// units, rounded down, as the scheduler's own reason words it.
func creditsCountdown(d time.Duration) string {
	return "in " + creditsSpan(d)
}

func creditsSpan(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d >= 24*time.Hour:
		return plural(int(d.Hours()/24), "day")
	case d >= time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(max(int(d.Minutes()), 1), "minute")
	}
}
