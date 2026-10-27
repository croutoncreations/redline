package notification

import (
	"fmt"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func EventTypes() []string {
	return []string{domain.EventRunStarted, domain.EventRunCompleted, domain.EventRunFailed, domain.EventSchedulerError, domain.EventResetExpiring, domain.EventResetRedeemed, domain.EventResetRedeemFailed, domain.EventFiresaleStarted, domain.EventFiresaleEnded, domain.EventResetAnnounced}
}

// sampleAnnouncement uses the real announcement builder with a fake post, so
// the sample shows exactly what a live reset.announced alert looks like.
func sampleAnnouncement(now time.Time) domain.NotificationEvent {
	published := now.Add(-10 * time.Minute)
	a := resetsignal.Announcement{
		Key: "test-announcement", Kind: resetsignal.KindCompleted, Account: "thsottiaux",
		Quote: "Sample announcement. No real reset happened.", QuoteSource: "original_post",
		URL: "https://x.com/thsottiaux/status/0", PublishedAt: &published, Feeds: []string{"resetbeacon"},
		Attribution: "Data: resetbeacon.com", AttributionURLs: "https://resetbeacon.com/",
	}
	e := a.Notification(now, false)
	e.Data["test"] = "true"
	e.Title = "Test: " + e.Title
	return e
}

// Sample is entirely synthetic: it never fetches usage or redeems a reset.
func Sample(kind, accountID, provider string, now time.Time) (domain.NotificationEvent, error) {
	if kind == domain.EventResetAnnounced {
		// Announcements are global, not tied to a provider account.
		return sampleAnnouncement(now), nil
	}
	e := domain.NotificationEvent{Version: 1, Type: kind, ProviderAccountID: accountID, OccurredAt: now.UTC(), Data: map[string]string{"test": "true"}}
	switch kind {
	case domain.EventResetExpiring:
		e.Data["reset_id"] = "test-reset"
		e.Data["count"], e.Data["action"], e.Data["lead"] = "1", "redeem_now", "24h"
		e.Data["expires_at"] = now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	case domain.EventResetRedeemed:
		e.Data["reset_id"], e.Data["request_id"] = "test-reset", "test-request"
		e.Data["outcome"], e.Data["code"], e.Data["reason"] = "redeemed", "reset", ""
		e.Data["verified"], e.Data["weekly_remaining"] = "true", "1"
	case domain.EventResetRedeemFailed:
		e.Data["reset_id"], e.Data["request_id"] = "test-reset", "test-request"
		e.Data["outcome"], e.Data["verified"] = "failed", "false"
		e.Data["reason"], e.Data["code"] = "expired unused", "expired"
	case domain.EventFiresaleStarted, domain.EventFiresaleEnded:
		e.Data["overflow"], e.Data["pace_gap"] = "0.38", "0.38"
		e.Data["unlocked_tier"] = string(domain.DispatchFiresale)
		if kind == domain.EventFiresaleEnded {
			e.Data["overflow"], e.Data["pace_gap"] = "0", "0"
			e.Data["unlocked_tier"] = ""
		}
		e.Data["effective_reset_at"], e.Data["effective_reset_source"] = now.Add(23*time.Hour).UTC().Format(time.RFC3339), "weekly"
		// Announcement-triggered, so a phone shows its Stop action. The key
		// matches no real sale, so tapping Stop on a test alert changes nothing.
		e.Data["trigger"], e.Data["announcement_key"] = "reset_announcement", "test-announcement"
		e.Data["account"], e.Data["deadline"], e.Data["mode"] = "thsottiaux", now.Add(2*time.Hour).UTC().Format(time.RFC3339), "notify"
		if kind == domain.EventFiresaleEnded {
			e.Data["reason"] = "stopped"
		}
	case domain.EventRunStarted:
		e.Title, e.Message = "Redline job started", "Sample job started."
	case domain.EventRunCompleted:
		e.Title, e.Message = "Redline job completed", "Sample job completed."
	case domain.EventRunFailed:
		e.Title, e.Message = "Redline job failed", "Sample job failed. No real job was run."
	case domain.EventSchedulerError:
		e.Title, e.Message = "Redline scheduler error", "Sample dispatch failure. No real job was run."
	default:
		return e, fmt.Errorf("unknown notification event %q", kind)
	}
	e = WithMessage(e, provider)
	e.Title = "Test: " + e.Title
	return e, nil
}
