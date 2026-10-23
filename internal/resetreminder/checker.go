package resetreminder

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
)

const (
	kindReminderPrefix = "remind:"
	kindExpiryAction   = "expiry_action"
	actionRemind       = "remind"
	actionRedeemNow    = "redeem_now"
)

type Store interface {
	RecordBankedResetNotice(context.Context, string, string, time.Time, string, time.Time) (bool, error)
}

type Notifier interface {
	Notify(context.Context, domain.NotificationEvent) error
}

type Checker struct {
	// Enabled includes event filtering: disabled checks must not claim pending notices.
	Enabled  bool
	Store    Store
	Notifier Notifier
	// AutoRedeems reports whether automatic redemption will attempt a reset
	// before it expires. In redeem mode the redeem-now notification is
	// suppressed only for deadlines whose resets all satisfy it; nil means
	// none do, so every expiring reset is notified.
	AutoRedeems func(decision.BankedReset) bool
}

func (r Checker) Check(ctx context.Context, providerID string, provider config.Provider, snapshot decision.UsageSnapshot, now time.Time) error {
	if !r.Enabled || r.Notifier == nil {
		return nil
	}
	resets := snapshot.BankedResetDetails
	if snapshot.BankedResets != nil {
		count := 0
		for _, reset := range resets {
			if reset.ResetsLeft > 0 {
				count += reset.ResetsLeft
			}
		}
		// Cached credit details may lag the authoritative aggregate after a redemption.
		if count != *snapshot.BankedResets {
			resets = nil
		}
	}
	if len(resets) == 0 && snapshot.BankedResetsExpireAt != nil && snapshot.BankedResets != nil && *snapshot.BankedResets > 0 {
		resets = []decision.BankedReset{{ID: "aggregate", ExpiresAt: snapshot.BankedResetsExpireAt, ResetsLeft: *snapshot.BankedResets}}
	}
	// A deadline, not a credit ID, is the notification identity. Keep the first ID
	// as informational payload while summing all remaining resets at that deadline.
	var deadlines []decision.BankedReset
	var autoHandled []bool
	byExpiry := make(map[time.Time]int)
	for _, reset := range resets {
		if reset.ResetsLeft <= 0 || reset.ExpiresAt == nil || !reset.ExpiresAt.After(now) {
			continue
		}
		auto := r.AutoRedeems != nil && r.AutoRedeems(reset)
		expires := reset.ExpiresAt.UTC()
		if i, ok := byExpiry[expires]; ok {
			deadlines[i].ResetsLeft += reset.ResetsLeft
			autoHandled[i] = autoHandled[i] && auto
		} else {
			byExpiry[expires] = len(deadlines)
			deadlines = append(deadlines, reset)
			autoHandled = append(autoHandled, auto)
		}
	}
	var errs []error
	for i, reset := range deadlines {
		action := provider.BankedResetOnExpiry()
		if action == "redeem" && !autoHandled[i] {
			// Automatic redemption will not attempt this deadline; warn instead.
			action = "notify"
		}
		if action != "off" && !now.Before(reset.ExpiresAt.Add(-provider.BankedResetRedeemLead())) {
			if action == "notify" {
				if err := r.send(ctx, providerID, provider, reset, now, provider.BankedResetRedeemLead(), kindExpiryAction, actionRedeemNow); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		var lead time.Duration
		for _, candidate := range provider.BankedResetRemindBefore() {
			if !now.Before(reset.ExpiresAt.Add(-candidate)) && (lead == 0 || candidate < lead) {
				lead = candidate
			}
		}
		if lead != 0 {
			if err := r.send(ctx, providerID, provider, reset, now, lead, kindReminderPrefix+resetDuration(lead), actionRemind); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (r Checker) send(ctx context.Context, providerID string, provider config.Provider, reset decision.BankedReset, now time.Time, lead time.Duration, kind, action string) error {
	inserted, err := r.Store.RecordBankedResetNotice(ctx, providerID, reset.ID, *reset.ExpiresAt, kind, now)
	if err != nil || !inserted {
		return err
	}
	name := provider.Provider
	switch strings.ToLower(name) {
	case "claude":
		name = "Claude"
	case "codex":
		name = "Codex"
	}
	message := fmt.Sprintf("%s banked reset expires in %s", name, resetDuration(reset.ExpiresAt.Sub(now)))
	if action == actionRedeemNow {
		message += " — redeem it now"
	}
	return r.Notifier.Notify(ctx, domain.NotificationEvent{
		Version: 1, Type: domain.EventResetExpiring, OccurredAt: now.UTC(), ProviderAccountID: providerID, Message: message,
		Data: map[string]string{"reset_id": reset.ID, "expires_at": reset.ExpiresAt.UTC().Format(time.RFC3339), "lead": resetDuration(lead), "action": action, "count": strconv.Itoa(reset.ResetsLeft)},
	})
}

func resetDuration(d time.Duration) string {
	value := d.String()
	// Trim seconds before minutes so an exact hour becomes "1h", not "1h0m".
	if strings.HasSuffix(value, "m0s") {
		value = strings.TrimSuffix(value, "0s")
	}
	if strings.HasSuffix(value, "h0m") {
		value = strings.TrimSuffix(value, "0m")
	}
	return value
}
