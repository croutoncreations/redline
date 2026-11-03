package notification_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

func TestReadableMessages(t *testing.T) {
	now := time.Date(2026, 7, 15, 19, 19, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, kind, provider, title, message string
		data                                 map[string]string
	}{
		{"expiry", domain.EventResetExpiring, "codex", "Codex banked reset expiring", "1 reset expires in 24h (Thu 19:19 UTC).", map[string]string{"count": "1", "expires_at": "2026-07-16T19:19:00Z"}},
		{"action", domain.EventResetExpiring, "claude", "Claude banked reset expiring", "2 resets expire in 23h (Thu 18:19 UTC). Redeem it now: redline reset redeem --provider codex-main", map[string]string{"count": "2", "expires_at": "2026-07-16T18:19:00Z", "action": "redeem_now"}},
		{"minutes UTC", domain.EventResetExpiring, "codex", "Codex banked reset expiring", "1 reset expires in 30m (Wed 19:49 UTC).", map[string]string{"count": "1", "expires_at": "2026-07-15T12:49:00-07:00"}},
		{"verified no clears", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Allowance refilled.", map[string]string{"verified": "true", "weekly_remaining": "1"}},
		{"verified unknown clears", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Allowance refilled.", map[string]string{"verified": "true", "cleared": "other"}},
		{"verified weekly no remaining", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Weekly allowance refilled.", map[string]string{"verified": "true", "cleared": "weekly"}},
		{"verified", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Weekly allowance refilled (now 100% remaining).", map[string]string{"verified": "true", "cleared": "weekly", "weekly_remaining": "1"}},
		{"verified both", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Weekly allowance refilled (now 90% remaining).", map[string]string{"verified": "true", "cleared": "five_hour,weekly", "weekly_remaining": "0.9", "five_hour_remaining": "1"}},
		{"verified short", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "5-hour window refilled (now 80% remaining).", map[string]string{"verified": "true", "cleared": "five_hour", "weekly_remaining": "0.1", "five_hour_remaining": "0.8"}},
		{"verified short no remaining", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "5-hour window refilled.", map[string]string{"verified": "true", "cleared": "five_hour", "five_hour_remaining": "invalid"}},
		{"unverified short", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Redeemed; the refill isn't visible yet. Check again in a few minutes.", map[string]string{"verified": "false", "cleared": "five_hour", "five_hour_remaining": "1"}},
		{"unverified", domain.EventResetRedeemed, "codex", "Codex reset redeemed", "Redeemed; the refill isn't visible yet. Check again in a few minutes.", nil},
		{"expired", domain.EventResetRedeemFailed, "codex", "Codex reset not redeemed", "Expired unused", map[string]string{"reason": "expired unused"}},
		{"unknown", domain.EventResetRedeemFailed, "codex", "Codex reset not redeemed", "Outcome unknown at expiry — check the provider", map[string]string{"reason": "redemption request outcome unknown at expiry; check the provider"}},
		{"failure", domain.EventResetRedeemFailed, "other", "codex-main reset not redeemed", "Please sign in again", map[string]string{"reason": "Please sign in again", "code": "auth_failed"}},
		{"firesale", domain.EventFiresaleStarted, "codex", "Codex firesale started", "38% of weekly capacity would go unused before Fri 17:41 UTC. All job tiers unlocked.", map[string]string{"overflow": "0.38", "effective_reset_at": "2026-07-17T17:41:00Z"}},
		{"banked", domain.EventFiresaleStarted, "codex", "Codex firesale started", "38% of weekly capacity would go unused before the banked reset at Fri 17:41 UTC. All job tiers unlocked.", map[string]string{"overflow": "0.38", "effective_reset_at": "2026-07-17T17:41:00Z", "effective_reset_source": "banked_reset"}},
		{"pace", domain.EventFiresaleStarted, "codex", "Codex firesale started", "38% behind weekly pace before Fri 17:41 UTC. All job tiers unlocked.", map[string]string{"pace_gap": "0.38", "effective_reset_at": "2026-07-17T17:41:00Z"}},
		{"pace mode", domain.EventFiresaleStarted, "codex", "Codex firesale started", "38% behind weekly pace before Fri 17:41 UTC. All job tiers unlocked.", map[string]string{"overflow": "0", "overflow_available": "false", "pace_gap": "0.38", "effective_reset_at": "2026-07-17T17:41:00Z"}},
		{"predicted", domain.EventFiresaleStarted, "codex", "Codex firesale started", "38% of weekly capacity would go unused before the predicted global reset at Fri 17:41 UTC. All job tiers unlocked.", map[string]string{"overflow": "0.38", "effective_reset_at": "2026-07-17T17:41:00Z", "effective_reset_source": "predicted_global"}},
		{"ended", domain.EventFiresaleEnded, "codex", "Codex firesale ended", "Back to normal pacing.", nil},
		{"usage refill", domain.EventFiresaleEnded, "codex", "Codex firesale ended", "Your weekly allowance refilled, so the reset has landed. Back to normal pacing.", map[string]string{"trigger": "reset_announcement", "reason": "completed", "completion_source": "usage_refill"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := notification.WithMessage(domain.NotificationEvent{Type: tt.kind, ProviderAccountID: "codex-main", OccurredAt: now, Data: tt.data}, tt.provider)
			if e.Title != tt.title || e.Message != tt.message {
				t.Fatalf("title=%q message=%q", e.Title, e.Message)
			}
		})
	}
}
