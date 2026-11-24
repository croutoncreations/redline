package apicredits

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

// Snapshot turns a credit grants observation into a scheduling snapshot.
//
// Spendable promotional credit becomes the account window: remaining is the
// fraction of the available promo grants left, resetting at the soonest
// expiry over that grant's own period, so the same pace and firesale logic
// that drives subscription windows applies. The dollar figures travel in
// Credits. Purchased credit is never part of the window.
func Snapshot(report FetchedReport, settings config.EffectiveAPICredits) (decision.UsageSnapshot, error) {
	if report.FetchedAt == nil {
		return decision.UsageSnapshot{}, fmt.Errorf("API credits have not been read yet")
	}
	credits := &decision.CreditsBalance{
		PromoRemaining: report.Promo.Remaining, PromoGranted: report.Promo.Granted,
		NextExpiring: report.Promo.NextExpiring, EffectiveAt: report.Promo.EffectiveAt, ExpiresAt: report.Promo.ExpiresAt,
		PaidAvailable: max(report.PaidAvailable, 0), MinPromoBalance: settings.MinPromoBalance,
		Pacing: settings.Pacing, FiresaleWindowSeconds: int64(settings.FiresaleWindow / time.Second),
	}
	for _, grant := range report.Grants {
		if grant.State != StateAvailable {
			continue
		}
		credits.Grants = append(credits.Grants, decision.CreditGrant{Kind: grant.Kind, Amount: grant.Amount,
			Remaining: grant.Remaining, EffectiveAt: grant.EffectiveAt, ExpiresAt: grant.ExpiresAt})
	}
	sort.SliceStable(credits.Grants, func(i, j int) bool {
		return credits.Grants[i].ExpiresAt.Before(*credits.Grants[j].ExpiresAt)
	})
	snapshot := decision.UsageSnapshot{
		Provider: config.ProviderAPICredits, ObservedAt: report.FetchedAt.UTC(), Source: "native",
		Confidence: "high", Credits: credits,
	}
	promo := report.Promo
	if promo.Remaining <= 0 || promo.Granted <= 0 || promo.ExpiresAt == nil {
		snapshot.WeeklyUnavailable = true
		return snapshot, snapshot.Validate()
	}
	period := int64(0)
	if promo.EffectiveAt != nil {
		period = int64(promo.ExpiresAt.Sub(*promo.EffectiveAt) / time.Second)
	}
	if period <= 0 {
		// A grant cycle is a billing month; without a start, assume one.
		period = int64(30 * 24 * time.Hour / time.Second)
	}
	snapshot.Weekly = decision.UsageWindow{
		Remaining: min(promo.Remaining/promo.Granted, 1), ResetsAt: promo.ExpiresAt.UTC(), PeriodSeconds: period,
	}
	return snapshot, snapshot.Validate()
}

// RawPayload is what is stored beside each snapshot: the derived report,
// with grant classifications but never grant or invoice IDs.
func RawPayload(report FetchedReport) ([]byte, error) {
	return json.Marshal(report)
}
