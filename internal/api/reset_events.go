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

func (s *Server) automaticBankedResets(ctx context.Context, providerID, provider string, details []decision.BankedReset) ([]decision.BankedReset, error) {
	attempts, err := s.store.AutomaticBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return nil, err
	}
	return s.availableAutomaticBankedResets(ctx, providerID, provider, details, attempts)
}

func (s *Server) availableAutomaticBankedResets(ctx context.Context, providerID, provider string, details []decision.BankedReset, attempts []store.AutomaticBankedResetRedemption) ([]decision.BankedReset, error) {
	completed, err := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
	if err != nil {
		return nil, err
	}
	consumed := make(map[string]bool, len(completed))
	for _, attempt := range completed {
		consumed[attempt.ResetID] = true
	}
	for _, attempt := range attempts {
		if attempt.Outcome == "gone" || attempt.Outcome == "redeemed" || attempt.Outcome == "unknown" {
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

func (s *Server) automaticResetState(ctx context.Context, providerID, name string, details []decision.BankedReset) (automaticResetState, error) {
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
	state.details, err = s.availableAutomaticBankedResets(ctx, providerID, name, details, state.attempts)
	return state, err
}

// nextAutomaticBankedReset is the single timing/selection rule for both spending
// and the effective horizon. A pending request owns the provider until resolved.
func (s *Server) nextAutomaticBankedReset(provider config.Provider, state automaticResetState) (decision.BankedReset, time.Time) {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	now := s.now()
	providerAt := now
	later := func(at time.Time) {
		if at.After(providerAt) {
			providerAt = at
		}
	}
	later(s.usageSources.NativeBlockedUntil(name))
	if state.pending.ID != 0 {
		later(state.pending.CreatedAt.Add(5 * time.Minute))
	}
	for _, a := range state.attempts {
		later(a.LastAttemptAt.Add(5 * time.Minute))
	}
	var selected decision.BankedReset
	var selectedAt time.Time
	for _, reset := range state.details {
		at := providerAt
		laterReset := func(deadline time.Time) {
			if deadline.After(at) {
				at = deadline
			}
		}
		if reset.ExpiresAt != nil {
			laterReset(reset.ExpiresAt.Add(-provider.BankedResetRedeemLead()))
		}
		if reset.CooldownUntil != nil {
			laterReset(*reset.CooldownUntil)
		}
		for _, a := range state.attempts {
			if a.ResetID == reset.ID {
				laterReset(a.RetryAt)
			}
		}
		if state.pending.ID == 0 && !automaticResetUsable(name, reset, at) {
			continue
		}
		if reset.ExpiresAt != nil && !at.Before(*reset.ExpiresAt) {
			continue
		}
		if selected.ID == "" || at.Before(selectedAt) {
			selected, selectedAt = reset, at
		}
	}
	return selected, selectedAt
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
	provider := s.config.Providers[providerID]
	if !s.automaticRedemptionEnabled(provider) {
		return events, nil
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	state, err := s.automaticResetState(ctx, providerID, name, snapshot.BankedResetDetails)
	if err != nil {
		return nil, err
	}
	reset, at := s.nextAutomaticBankedReset(provider, state)
	if reset.ID != "" && reset.ExpiresAt != nil && at.Before(snapshot.Weekly.ResetsAt) && slices.Contains(reset.Clears, "weekly") {
		events = append(events, decision.ResetEvent{Source: decision.ResetBankedReset, At: at, Clears: reset.Clears, Confidence: "high", Detail: reset.ID})
	}
	return events, nil
}
