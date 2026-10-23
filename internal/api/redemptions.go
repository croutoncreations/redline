package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
)

type bankedResetRedeemer interface {
	Prepare(context.Context, config.Provider, string, bool) (nativeusage.RedeemRequest, error)
	Redeem(context.Context, config.Provider, nativeusage.RedeemRequest) (nativeusage.RedeemResult, error)
}

// SelectBankedReset is shared with the CLI confirmation, so the prompt describes
// the same selection rules as the spending operation.
func SelectBankedReset(provider string, details []decision.BankedReset, now time.Time) (decision.BankedReset, error) {
	var selected *decision.BankedReset
	if provider == "codex" && len(details) == 0 {
		return decision.BankedReset{}, fmt.Errorf("credit details not loaded yet; try again shortly")
	}
	for _, detail := range details {
		if detail.ResetsLeft <= 0 || detail.ID == "" || (detail.ExpiresAt != nil && !detail.ExpiresAt.After(now)) {
			continue
		}
		if provider == "claude" && !detail.Claimable {
			continue
		}
		if selected == nil || (detail.ExpiresAt != nil && (selected.ExpiresAt == nil || detail.ExpiresAt.Before(*selected.ExpiresAt))) {
			d := detail
			selected = &d
		}
	}
	if selected == nil {
		return decision.BankedReset{}, fmt.Errorf("no claimable banked reset is available")
	}
	if provider == "claude" {
		if selected.CooldownUntil != nil && selected.CooldownUntil.After(now) {
			return *selected, fmt.Errorf("reset is in cooldown until %s", selected.CooldownUntil.Format(time.RFC3339))
		}
		if selected.UsableNow == nil || !*selected.UsableNow {
			return *selected, fmt.Errorf("Claude did not report this reset as usable now")
		}
	}
	return *selected, nil
}

func (s *Server) RedeemBankedReset(ctx context.Context, providerID, trigger string, dryRun bool) (nativeusage.RedeemResult, error) {
	return s.redeemBankedResetExpected(ctx, providerID, trigger, dryRun, "")
}

func (s *Server) redeemBankedResetExpected(ctx context.Context, providerID, trigger string, dryRun bool, expectedResetID string) (nativeusage.RedeemResult, error) {
	result := nativeusage.RedeemResult{Outcome: "failed"}
	if s.redemptionDisabled {
		return result, fmt.Errorf("%w: banked reset redemption is disabled in demo mode", store.ErrConflict)
	}
	provider, ok := s.config.Providers[providerID]
	if !ok {
		return result, fmt.Errorf("provider %q is not configured", providerID)
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	if name != "claude" && name != "codex" {
		return result, fmt.Errorf("banked resets for %q are unsupported", name)
	}
	if trigger != "manual" && trigger != "automatic" && trigger != "dry_run" {
		return result, fmt.Errorf("trigger must be manual, automatic, or dry_run")
	}
	if dryRun {
		trigger = "dry_run"
	}
	s.redemptionMu.Lock()
	if s.redemptionsInFlight[providerID] {
		s.redemptionMu.Unlock()
		return result, fmt.Errorf("%w: a redemption is already in progress", store.ErrConflict)
	}
	if s.redemptionsInFlight == nil {
		s.redemptionsInFlight = make(map[string]bool)
	}
	s.redemptionsInFlight[providerID] = true
	s.redemptionMu.Unlock()
	defer func() { s.redemptionMu.Lock(); delete(s.redemptionsInFlight, providerID); s.redemptionMu.Unlock() }()
	if s.usageSources.SkipBankedResetProviders[name] {
		return result, fmt.Errorf("%w: cannot redeem %s resets with multiple accounts configured; use the provider CLI", store.ErrConflict, name)
	}
	// A confirmed retry of a reset that already redeemed replays the stored
	// result before lockouts or telemetry are consulted: current usage may no
	// longer list it, and a fresh Claude request key would spend again.
	if !dryRun && expectedResetID != "" {
		pending, err := s.store.PendingBankedResetRedemption(ctx, providerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return result, err
		}
		if pending.ID == 0 {
			completed, err := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
			if err != nil {
				return result, err
			}
			for _, attempt := range completed {
				if attempt.ResetID == expectedResetID {
					return replayedRedemption(attempt), nil
				}
			}
		}
	}
	if until := s.usageSources.NativeBlockedUntil(name); s.now().Before(until) {
		result.Outcome = "cooldown"
		result.CooldownUntil = &until
		result.Reason = "provider rate-limit lockout is active"
		if !dryRun {
			s.notifyRedemption(ctx, providerID, result)
		}
		return result, nil
	}
	var pre decision.UsageSnapshot
	var selected decision.BankedReset
	var pending store.BankedResetRedemption
	if !dryRun {
		var err error
		pending, err = s.store.PendingBankedResetRedemption(ctx, providerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return result, err
		}
		if pending.ID != 0 && expectedResetID != "" && expectedResetID != pending.ResetID {
			return result, fmt.Errorf("%w: previous redemption of %s is unresolved; retry it before selecting another reset", store.ErrConflict, pending.ResetID)
		}
	}
	if !dryRun {
		var err error
		pre, _, err = s.store.LatestSnapshot(ctx, name)
		if err != nil {
			return result, err
		}
		if pending.ID != 0 {
			selected.ID = pending.ResetID
		} else {
			details := pre.BankedResetDetails
			var replay *store.BankedResetRedemption
			if name == "codex" {
				completed, lookupErr := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
				if lookupErr != nil {
					return result, lookupErr
				}
				consumed := make(map[string]bool, len(completed))
				for _, a := range completed {
					consumed[a.ResetID] = true
					if replay == nil {
						for _, detail := range details {
							if detail.ID == a.ResetID {
								copy := a
								replay = &copy
								break
							}
						}
					}
				}
				available := make([]decision.BankedReset, 0, len(details))
				for _, detail := range details {
					if !consumed[detail.ID] {
						available = append(available, detail)
					}
				}
				details = available
			}
			if len(details) == 0 && replay != nil {
				if expectedResetID != "" && expectedResetID != replay.ResetID {
					return result, fmt.Errorf("%w: the available reset changed from %q to %q; re-run to confirm", store.ErrConflict, expectedResetID, replay.ResetID)
				}
				return replayedRedemption(*replay), nil
			}
			selected, err = SelectBankedReset(name, details, s.now())
		}
		if err != nil {
			result.Outcome = "unavailable"
			result.Reason = err.Error()
			result.ResetID = selected.ID
			result.CooldownUntil = selected.CooldownUntil
			if selected.CooldownUntil != nil && selected.CooldownUntil.After(s.now()) {
				result.Outcome = "cooldown"
			}
			s.notifyRedemption(ctx, providerID, result)
			return result, nil
		}
	}
	if !dryRun && expectedResetID != "" && expectedResetID != selected.ID {
		return result, fmt.Errorf("%w: the available reset changed from %q to %q; re-run to confirm", store.ErrConflict, expectedResetID, selected.ID)
	}
	req := nativeusage.RedeemRequest{ResetID: pending.ResetID, RequestID: pending.RequestID}
	var err error
	if pending.ID == 0 {
		req, err = s.redeemer.Prepare(ctx, provider, selected.ID, dryRun)
	}
	if err != nil {
		result.Reason = err.Error()
		if !dryRun {
			s.notifyRedemption(ctx, providerID, result)
		}
		return result, nil
	}
	attempt, err := s.store.BeginBankedResetRedemption(ctx, store.BankedResetRedemption{ProviderAccountID: providerID, ResetID: req.ResetID, RequestID: req.RequestID, Trigger: trigger, CreatedAt: s.now().UTC()})
	if err != nil {
		return result, err
	}
	if attempt.State == "done" && attempt.Outcome == "redeemed" {
		return replayedRedemption(attempt), nil
	}
	req.RequestID = attempt.RequestID
	result, err = s.redeemer.Redeem(ctx, provider, req)
	result.ResetID, result.RequestID = req.ResetID, req.RequestID
	if err != nil {
		// Keep pending on ambiguous transport failures/429s. The next attempt
		// must reuse the persisted request ID, even if the response was lost.
		s.usageSources.RecordNativeError(name, err)
		result.Outcome = "failed"
		result.Reason = err.Error()
		if recordErr := s.store.RecordBankedResetRedemptionError(context.WithoutCancel(ctx), attempt.ID, result.Reason); recordErr != nil {
			return result, fmt.Errorf("%w (recording redemption failure: %v)", err, recordErr)
		}
		if !dryRun {
			s.notifyRedemption(ctx, providerID, result)
		}
		return result, err
	}
	completedAt := s.now().UTC()
	if result.Outcome == "redeemed" && !dryRun {
		post, raw, refreshErr := s.usageSources.FetchFresh(ctx, providerID, provider)
		if refreshErr == nil {
			refreshErr = s.store.SaveSnapshot(ctx, post, raw)
		}
		result.Verified = refreshErr == nil && post.ObservedAt.After(completedAt) && (post.Weekly.Remaining > pre.Weekly.Remaining || post.Weekly.ResetsAt.After(pre.Weekly.ResetsAt))
		if !result.Verified {
			result.Reason = strings.TrimSpace(result.Reason + " Usage has not refilled yet; refresh and check again. Codex may still enforce the old window (known provider bug).")
		}
	}
	if err := s.store.CompleteBankedResetRedemption(context.WithoutCancel(ctx), attempt.ID, result.Outcome, result.Code, result.Reason, completedAt); err != nil {
		return result, err
	}
	if !dryRun {
		s.notifyRedemption(ctx, providerID, result)
	}
	return result, nil
}

func replayedRedemption(a store.BankedResetRedemption) nativeusage.RedeemResult {
	return nativeusage.RedeemResult{Outcome: a.Outcome, Code: a.Code, Reason: a.Reason, ResetID: a.ResetID, RequestID: a.RequestID, CompletedAt: a.CompletedAt, Replayed: true}
}

func (s *Server) notifyRedemption(ctx context.Context, providerID string, result nativeusage.RedeemResult) {
	if s.notifier == nil || result.Outcome == "nothing_to_reset" || result.Outcome == "dry_run_ok" {
		return
	}
	event := domain.EventResetRedeemFailed
	if result.Outcome == "redeemed" {
		event = domain.EventResetRedeemed
	}
	if err := s.notifier.Notify(ctx, domain.NotificationEvent{Version: 1, Type: event, OccurredAt: s.now().UTC(), ProviderAccountID: providerID, Message: "Banked reset: " + result.Outcome, Data: map[string]string{"reset_id": result.ResetID, "request_id": result.RequestID, "outcome": result.Outcome, "code": result.Code, "reason": result.Reason, "verified": strconv.FormatBool(result.Verified)}}); err != nil {
		log.Printf("redline %s redemption notification failed: %v", providerID, err)
	}
}

func (s *Server) redeemBankedReset(w http.ResponseWriter, r *http.Request) {
	if authorized, viaCookie := s.authorized(r); !authorized || viaCookie {
		writeJSON(w, http.StatusForbidden, problem{Error: "redeeming banked resets requires the local API token"})
		return
	}
	var body *struct {
		DryRun          bool   `json:"dry_run"`
		ExpectedResetID string `json:"expected_reset_id,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if body == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "request body must be an object"})
		return
	}
	result, err := s.redeemBankedResetExpected(r.Context(), r.PathValue("provider"), "manual", body.DryRun, body.ExpectedResetID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) bankedResetRedemptions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider")
	if _, ok := s.config.Providers[id]; !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	attempts, err := s.store.BankedResetRedemptions(r.Context(), id, 20)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempts)
}
