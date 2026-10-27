package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/store"
)

func (s *Server) automaticBankedResets(ctx context.Context, providerID, provider string, details []decision.BankedReset, observedAt time.Time) ([]decision.BankedReset, error) {
	attempts, err := s.store.AutomaticBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return nil, err
	}
	return s.availableAutomaticBankedResets(ctx, providerID, provider, details, observedAt, attempts)
}

func (s *Server) availableAutomaticBankedResets(ctx context.Context, providerID, provider string, details []decision.BankedReset, observedAt time.Time, attempts []store.AutomaticBankedResetRedemption) ([]decision.BankedReset, error) {
	completed, err := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return nil, err
	}
	consumed := consumedBankedResets(completed, details, observedAt)
	for _, attempt := range attempts {
		if attempt.Outcome == "gone" || attempt.Outcome == "unknown" {
			consumed[attempt.ResetID] = true
		}
	}
	available := make([]decision.BankedReset, 0, len(details))
	for _, reset := range details {
		if !consumed[reset.ID] && !(provider == "claude" && reset.RequiresLimit != nil && *reset.RequiresLimit) {
			available = append(available, reset)
		}
	}
	return available, nil
}

type automaticResetState struct {
	attempts []store.AutomaticBankedResetRedemption
	pending  store.BankedResetRedemption
	details  []decision.BankedReset
}

func (s *Server) automaticResetState(ctx context.Context, providerID, name string, details []decision.BankedReset, observedAt time.Time) (automaticResetState, error) {
	var state automaticResetState
	var err error
	state.attempts, err = s.store.AutomaticBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return state, err
	}
	state.pending, err = s.store.PendingBankedResetRedemption(ctx, providerID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return state, err
	}
	if state.pending.ID != 0 {
		reset := decision.BankedReset{ID: state.pending.ResetID}
		for _, detail := range details {
			if detail.ID == reset.ID {
				reset = detail
				break
			}
		}
		for _, a := range state.attempts {
			if a.ResetID == reset.ID && !a.ExpiresAt.IsZero() {
				expiry := a.ExpiresAt
				reset.ExpiresAt = &expiry
				break
			}
		}
		if reset.ExpiresAt == nil {
			reset.ExpiresAt, err = s.store.LastBankedResetExpiry(ctx, name, reset.ID)
			if err != nil {
				return state, err
			}
		}
		state.details = []decision.BankedReset{reset}
		return state, nil
	}
	// Retain full consumed history for now: a stale snapshot must never promise
	// a second refill. Candidate-ID queries could bound dashboard reads later.
	state.details, err = s.availableAutomaticBankedResets(ctx, providerID, name, details, observedAt, state.attempts)
	return state, err
}

// nextAutomaticBankedReset is the single timing/selection rule for both spending
// and the effective horizon. A pending request owns the provider until resolved.
func (s *Server) nextAutomaticBankedReset(provider config.Provider, state automaticResetState) (decision.BankedReset, time.Time) {
	var selected decision.BankedReset
	var selectedAt time.Time
	for _, reset := range state.details {
		at, ok := s.automaticAttemptAt(provider, state, reset)
		if ok && (selected.ID == "" || at.Before(selectedAt)) {
			selected, selectedAt = reset, at
		}
	}
	return selected, selectedAt
}

// automaticAttemptAt is the earliest time automatic redemption may attempt
// reset, and whether that is before it expires. Reminders use the same rule to
// decide whether redeem mode will really handle a deadline.
func (s *Server) automaticAttemptAt(provider config.Provider, state automaticResetState, reset decision.BankedReset) (time.Time, bool) {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	at := s.now()
	later := func(deadline time.Time) {
		if deadline.After(at) {
			at = deadline
		}
	}
	later(s.usageSources.NativeBlockedUntil(name))
	if state.pending.ID != 0 {
		later(state.pending.CreatedAt.Add(5 * time.Minute))
	}
	for _, a := range state.attempts {
		later(a.LastAttemptAt.Add(5 * time.Minute))
	}
	if reset.ExpiresAt != nil {
		later(reset.ExpiresAt.Add(-provider.BankedResetRedeemLead()))
	}
	if reset.CooldownUntil != nil {
		later(*reset.CooldownUntil)
	}
	for _, a := range state.attempts {
		if a.ResetID == reset.ID {
			later(a.RetryAt)
		}
	}
	if state.pending.ID == 0 && !automaticResetUsable(name, reset, at) {
		return at, false
	}
	if reset.ExpiresAt != nil && !at.Before(*reset.ExpiresAt) {
		return at, false
	}
	return at, true
}

// automaticallyRedeemable reports the reset IDs that automatic redemption will
// attempt before they expire. Redeem mode suppresses redeem-now reminders only
// for these; every other expiring reset falls back to a notification.
func (s *Server) automaticallyRedeemable(ctx context.Context, providerID string, provider config.Provider, snapshot decision.UsageSnapshot) (map[string]bool, error) {
	if !s.automaticRedemptionEnabled(provider) {
		return nil, nil
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	state, err := s.automaticResetState(ctx, providerID, name, snapshot.BankedResetDetails, snapshot.ObservedAt)
	if err != nil {
		return nil, err
	}
	handled := make(map[string]bool, len(state.details))
	for _, reset := range state.details {
		if _, ok := s.automaticAttemptAt(provider, state, reset); ok {
			handled[reset.ID] = true
		}
	}
	return handled, nil
}

func automaticResetUsable(provider string, reset decision.BankedReset, at time.Time) bool {
	if reset.ID == "" || reset.ResetsLeft <= 0 || reset.ExpiresAt == nil || !reset.ExpiresAt.After(at) {
		return false
	}
	if provider == "claude" {
		return reset.Claimable && reset.UsableNow != nil && *reset.UsableNow &&
			(reset.RequiresLimit == nil || !*reset.RequiresLimit) &&
			(reset.CooldownUntil == nil || !reset.CooldownUntil.After(at))
	}
	return provider == "codex"
}

func (s *Server) automaticRedemptionEnabled(provider config.Provider) bool {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	return provider.BankedResetOnExpiry() == "redeem" && !s.redemptionDisabled &&
		s.config.UsageMonitor.Enabled && !s.usageSources.SkipBankedResetProviders[name]
}

func (s *Server) providerResetEvents(ctx context.Context, providerID string, snapshot decision.UsageSnapshot) ([]decision.ResetEvent, error) {
	events := []decision.ResetEvent{{Source: decision.ResetWeekly, At: snapshot.Weekly.ResetsAt, Clears: []string{"weekly"}, Confidence: snapshot.Confidence}}
	if err := s.reconcileResetSignalFiresales(ctx); err != nil {
		return nil, err
	}
	provider := s.config.Providers[providerID]
	if strings.EqualFold(strings.TrimSpace(provider.Provider), "codex") && s.config.ResetSignalsFiresale() != "off" && !s.redemptionDisabled {
		sales, err := s.store.ActiveResetSignalFiresales(ctx)
		if err != nil {
			return nil, err
		}
		for _, sale := range sales {
			if sale.ProviderAccountID == providerID && sale.Deadline.After(s.now()) {
				events = append(events, decision.ResetEvent{Source: decision.ResetPredictedGlobal, At: sale.Deadline, Clears: []string{"weekly"}, Confidence: "announced", Detail: sale.AnnouncementKey})
			}
		}
	}
	if !s.automaticRedemptionEnabled(provider) {
		return events, nil
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	state, err := s.automaticResetState(ctx, providerID, name, snapshot.BankedResetDetails, snapshot.ObservedAt)
	if err != nil {
		return nil, err
	}
	reset, at := s.nextAutomaticBankedReset(provider, state)
	if reset.ID != "" && reset.ExpiresAt != nil && at.Before(snapshot.Weekly.ResetsAt) && slices.Contains(reset.Clears, "weekly") {
		events = append(events, decision.ResetEvent{Source: decision.ResetBankedReset, At: at, Clears: reset.Clears, Confidence: "high", Detail: reset.ID})
	}
	return events, nil
}
