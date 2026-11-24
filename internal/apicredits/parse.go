// Package apicredits reads OpenAI API Platform credit grants: the monthly
// API credits that eligible ChatGPT subscriptions grant to a linked API
// organization, alongside purchased and auto-recharge credit.
//
// The grant list is only available from the platform dashboard endpoint
// /v1/dashboard/billing/credit_grants, which accepts a browser session key
// (sess-...) and rejects API keys and Codex sign-ins.
package apicredits

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Grant kinds, derived from the grant ID prefix.
const (
	KindPromo        = "promo"         // chatgpt_api_credits_promo_: subscription benefit
	KindAutoRecharge = "auto_recharge" // auto_recharge_grant_: Auto Recharge purchase
	KindPurchase     = "purchase"      // in_: one-off purchase invoice
	KindOther        = "other"
)

// Grant states, matching the platform's Credit grants page.
const (
	StateAvailable = "available"
	StateInactive  = "inactive"
)

var kindPrefixes = []struct{ prefix, kind string }{
	{"chatgpt_api_credits_promo_", KindPromo},
	{"auto_recharge_grant_", KindAutoRecharge},
	{"in_", KindPurchase},
}

// Grant is one credit grant. Amounts are US dollars. The grant ID is not
// kept: it identifies the account's billing records and is not needed for
// display or scheduling.
type Grant struct {
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Amount      float64    `json:"amount"`
	Used        float64    `json:"used"`
	Remaining   float64    `json:"remaining"`
	EffectiveAt *time.Time `json:"effective_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// PromoBalance is the subscription (promotional) credit that is currently
// spendable, summed over available promo grants. ExpiresAt and EffectiveAt
// describe the soonest-expiring available promo grant.
type PromoBalance struct {
	Granted   float64 `json:"granted"`
	Remaining float64 `json:"remaining"`
	// NextExpiring is what is left on the soonest-expiring grant: the
	// amount that lapses at ExpiresAt if unused.
	NextExpiring float64    `json:"next_expiring"`
	EffectiveAt  *time.Time `json:"effective_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// Report is one observation of an organization's credit grants.
type Report struct {
	Promo PromoBalance `json:"promo"`
	// PaidAvailable is purchased and auto-recharge credit. It is shown, but
	// is never treated as schedulable capacity.
	PaidAvailable  float64 `json:"paid_available"`
	TotalAvailable float64 `json:"total_available"`
	Grants         []Grant `json:"grants"`
}

type grantPayload struct {
	ID                         string   `json:"id"`
	GrantAmount                float64  `json:"grant_amount"`
	UsedAmount                 float64  `json:"used_amount"`
	EffectiveAt                *float64 `json:"effective_at"`
	ExpiresAt                  *float64 `json:"expires_at"`
	IsScheduled                bool     `json:"is_scheduled"`
	IsHeldForPrepaidTransition bool     `json:"is_held_for_prepaid_transition"`
}

type summaryPayload struct {
	Object             string  `json:"object"`
	TotalAvailable     float64 `json:"total_available"`
	TotalPaidAvailable float64 `json:"total_paid_available"`
	Grants             *struct {
		Data []grantPayload `json:"data"`
	} `json:"grants"`
}

// Parse decodes a credit_grants response observed at now.
func Parse(body []byte, now time.Time) (Report, error) {
	var payload summaryPayload
	// Errors are fixed strings: a response body is never echoed.
	if err := json.Unmarshal(body, &payload); err != nil || payload.Object != "credit_summary" || payload.Grants == nil {
		return Report{}, fmt.Errorf("invalid credit grants response")
	}
	report := Report{
		PaidAvailable:  payload.TotalPaidAvailable,
		TotalAvailable: payload.TotalAvailable,
		Grants:         make([]Grant, 0, len(payload.Grants.Data)),
	}
	for _, raw := range payload.Grants.Data {
		grant := Grant{
			Kind:        kindOf(raw.ID),
			State:       StateInactive,
			Amount:      raw.GrantAmount,
			Used:        raw.UsedAmount,
			Remaining:   max(raw.GrantAmount-raw.UsedAmount, 0),
			EffectiveAt: epoch(raw.EffectiveAt),
			ExpiresAt:   epoch(raw.ExpiresAt),
		}
		if spendable(raw, grant, now) {
			grant.State = StateAvailable
		}
		report.Grants = append(report.Grants, grant)
		if grant.Kind != KindPromo || grant.State != StateAvailable {
			continue
		}
		report.Promo.Granted += grant.Amount
		report.Promo.Remaining += grant.Remaining
		if report.Promo.ExpiresAt == nil || grant.ExpiresAt.Before(*report.Promo.ExpiresAt) {
			report.Promo.ExpiresAt = grant.ExpiresAt
			report.Promo.EffectiveAt = grant.EffectiveAt
			report.Promo.NextExpiring = grant.Remaining
		}
	}
	return report, nil
}

// spendable reports whether a grant can be drawn on at now. A used-up grant
// reports no expiry, so a missing expiry is never read as "never expires".
// Scheduled and held grants are excluded until their meaning is confirmed.
func spendable(raw grantPayload, grant Grant, now time.Time) bool {
	switch {
	case grant.Remaining <= 0, grant.ExpiresAt == nil, !now.Before(*grant.ExpiresAt):
		return false
	case grant.EffectiveAt != nil && now.Before(*grant.EffectiveAt):
		return false
	case raw.IsScheduled, raw.IsHeldForPrepaidTransition:
		return false
	}
	return true
}

func kindOf(id string) string {
	for _, candidate := range kindPrefixes {
		if strings.HasPrefix(id, candidate.prefix) {
			return candidate.kind
		}
	}
	return KindOther
}

func epoch(seconds *float64) *time.Time {
	if seconds == nil {
		return nil
	}
	whole := int64(*seconds)
	t := time.Unix(whole, int64((*seconds-float64(whole))*1e9)).UTC()
	return &t
}
