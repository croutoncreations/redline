package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func (s *Server) redeemExpiringBankedReset(ctx context.Context, providerID string, provider config.Provider, snapshot decision.UsageSnapshot) error {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	if !s.automaticRedemptionEnabled(provider) {
		return nil
	}
	now := s.now()
	state, err := s.automaticResetState(ctx, providerID, name, snapshot.BankedResetDetails)
	if err != nil {
		return err
	}
	if state.pending.ID != 0 {
		found := false
		for i, a := range state.attempts {
			if a.ResetID == state.pending.ResetID {
				found = true
				if expiry := state.details[0].ExpiresAt; a.ExpiresAt.IsZero() && expiry != nil {
					a.ExpiresAt = *expiry
					if err := s.store.SaveAutomaticBankedResetRedemption(ctx, a); err != nil {
						return err
					}
					state.attempts[i] = a
				}
				break
			}
		}
		if !found {
			a := store.AutomaticBankedResetRedemption{ProviderAccountID: providerID, ResetID: state.pending.ResetID, LastAttemptAt: state.pending.CreatedAt, RetryAt: state.pending.CreatedAt.Add(5 * time.Minute), Outcome: "failed"}
			if expiry := state.details[0].ExpiresAt; expiry != nil {
				a.ExpiresAt = *expiry
			}
			if err := s.store.SaveAutomaticBankedResetRedemption(ctx, a); err != nil {
				return err
			}
			state.attempts = append(state.attempts, a)
		}
	}
	completed, err := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return err
	}
	consumed := make(map[string]bool, len(completed))
	for _, a := range completed {
		consumed[a.ResetID] = true
	}
	finalizedPending := false
	for _, a := range state.attempts {
		expired := !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(now)
		// Without an expiry, ambiguous manual requests still need a finite
		// recovery window. Never abandon the key merely because telemetry lost it.
		if a.ExpiresAt.IsZero() && state.pending.ResetID == a.ResetID && !now.Before(state.pending.CreatedAt.Add(24*time.Hour)) {
			expired = true
		}
		// The current pending request is always finalized once expired: an
		// earlier terminal outcome for the same reset does not resolve it, and
		// leaving it pending would block every later reset.
		isPending := state.pending.ID != 0 && state.pending.ResetID == a.ResetID
		if expired && (isPending || (a.Outcome != "redeemed" && a.Outcome != "gone" && !consumed[a.ResetID])) {
			if err := s.expiredBankedReset(ctx, a, now); err != nil {
				return err
			}
			if a.ResetID == state.pending.ResetID {
				finalizedPending = true
			}
		}
	}
	if finalizedPending {
		state, err = s.automaticResetState(ctx, providerID, name, snapshot.BankedResetDetails)
		if err != nil {
			return err
		}
	}
	selected, at := s.nextAutomaticBankedReset(provider, state)
	if selected.ID == "" || now.Before(at) {
		return nil
	}
	a := store.AutomaticBankedResetRedemption{ProviderAccountID: providerID, ResetID: selected.ID, LastAttemptAt: now, RetryAt: now.Add(5 * time.Minute), Outcome: "failed"}
	if selected.ExpiresAt != nil {
		a.ExpiresAt = *selected.ExpiresAt
	}
	// Persist throttling before spending, including failures before the engine
	// can write an attempt. Pause only controls dispatch, never expiring resets.
	if err := s.store.SaveAutomaticBankedResetRedemption(ctx, a); err != nil {
		return err
	}
	result, redeemErr := s.redeemBankedResetExpected(ctx, providerID, "automatic", false, selected.ID)
	a.Outcome = result.Outcome
	if redeemErr != nil {
		a.Outcome = "failed"
	}
	if result.Outcome == "nothing_to_reset" {
		a.RetryAt = now.Add(30 * time.Minute)
	}
	if result.CooldownUntil != nil && result.CooldownUntil.After(a.RetryAt) {
		a.RetryAt = *result.CooldownUntil
	}
	if until := s.usageSources.NativeBlockedUntil(name); until.After(a.RetryAt) {
		a.RetryAt = until
	}
	// The engine records errors and emits outcome notifications. A failed
	// redemption must not turn a successful usage observation into a fetch error.
	return s.store.SaveAutomaticBankedResetRedemption(context.WithoutCancel(ctx), a)
}

func (s *Server) expiredBankedReset(ctx context.Context, a store.AutomaticBankedResetRedemption, now time.Time) error {
	pending, err := s.store.PendingBankedResetRedemption(ctx, a.ProviderAccountID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	reason := "expired unused"
	if a.Outcome == "unknown" || (pending.ID != 0 && pending.ResetID == a.ResetID) {
		reason = "redemption request outcome unknown at expiry; check the provider"
		a.Outcome = "unknown"
		if err := s.store.SaveAutomaticBankedResetRedemption(ctx, a); err != nil {
			return err
		}
		if pending.ID != 0 && pending.ResetID == a.ResetID {
			if err := s.store.CompleteBankedResetRedemption(ctx, pending.ID, "unknown", pending.Code, reason, now); err != nil {
				return err
			}
		}
	}
	if s.notifier == nil || !s.config.Notifications.Enabled || !s.config.NotificationEvents()[domain.EventResetRedeemFailed] {
		return nil
	}
	noticeAt := a.ExpiresAt
	if noticeAt.IsZero() {
		noticeAt = pending.CreatedAt.Add(24 * time.Hour)
	}
	// Unknown outcomes use their own notice kind so a known-unused reset that
	// shares the same expiry can never suppress the warning to check the provider.
	kind := "expired_unredeemed"
	if a.Outcome == "unknown" {
		kind = "expired_outcome_unknown"
	}
	claimed, err := s.store.RecordBankedResetNotice(ctx, a.ProviderAccountID, a.ResetID, noticeAt, kind, now)
	if err != nil || !claimed {
		return err
	}
	data := map[string]string{"reset_id": a.ResetID, "reason": reason, "outcome": a.Outcome}
	if !a.ExpiresAt.IsZero() {
		data["expires_at"] = a.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return s.notifier.Notify(ctx, domain.NotificationEvent{Version: 1, Type: domain.EventResetRedeemFailed, OccurredAt: now.UTC(), ProviderAccountID: a.ProviderAccountID, Message: "Banked reset: " + reason, Data: data})
}
