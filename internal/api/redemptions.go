package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/store"
	"github.com/google/uuid"
)

// redemptionGenerationNamespace derives the next deterministic request key
// after a completed non-redemption reused a provider's deterministic key.
var redemptionGenerationNamespace = uuid.MustParse("5f0c2a8e-3b7d-4e0a-9c61-7a2d4b8e1f93")

type bankedResetRedeemer interface {
	Prepare(context.Context, config.Provider, string, bool) (nativeusage.RedeemRequest, error)
	Redeem(context.Context, config.Provider, nativeusage.RedeemRequest) (nativeusage.RedeemResult, error)
}

// ResetChoice narrows which reset to spend. Limit is the limit the account
// has reached (decision.ReachedLimit); Kind is a reset kind the user asked
// for. Both empty keeps the provider's own next reset.
type ResetChoice struct {
	Limit string
	Kind  string
}

// SelectBankedReset is shared with the CLI confirmation, so the prompt describes
// the same selection rules as the spending operation.
func SelectBankedReset(provider string, details []decision.BankedReset, now time.Time) (decision.BankedReset, error) {
	return SelectBankedResetFor(provider, details, now, ResetChoice{})
}

// SelectBankedResetFor picks the reset to spend. For Claude with a reached
// limit or a requested kind it chooses by kind the way Claude Code's
// /limit-reset does: the kind made for the reached limit first (a Fable reset
// when only the Fable allowance is used up), broader kinds after, and at the
// weekly limit a Fable reset last because it only gives back what Fable used.
// Within a kind it prefers Claude's next grant, then the soonest expiry.
func SelectBankedResetFor(provider string, details []decision.BankedReset, now time.Time, choice ResetChoice) (decision.BankedReset, error) {
	if choice.Kind != "" && provider != "claude" {
		return decision.BankedReset{}, fmt.Errorf("choosing a reset kind is only supported for Claude")
	}
	if provider != "claude" || (choice.Limit == "" && choice.Kind == "") {
		return selectNextBankedReset(provider, details, now)
	}
	usable := func(d decision.BankedReset) bool {
		return d.ID != "" && d.Spendable(now) && (d.ExpiresAt == nil || d.ExpiresAt.After(now)) &&
			d.UsableNow != nil && *d.UsableNow && (d.CooldownUntil == nil || !d.CooldownUntil.After(now))
	}
	pick := func(kind string) *decision.BankedReset {
		var best *decision.BankedReset
		for _, d := range details {
			if d.Kind != kind || !usable(d) || (choice.Limit != "" && !d.RefillsLimit(choice.Limit)) {
				continue
			}
			better := best == nil || (d.Claimable && !best.Claimable) ||
				(d.Claimable == best.Claimable && d.ExpiresAt != nil && (best.ExpiresAt == nil || d.ExpiresAt.Before(*best.ExpiresAt)))
			if better {
				copy := d
				best = &copy
			}
		}
		return best
	}
	order := decision.ResetKindOrder(choice.Limit)
	if choice.Kind != "" {
		order = []string{choice.Kind}
	}
	for _, kind := range order {
		if reset := pick(kind); reset != nil {
			return *reset, nil
		}
	}
	return decision.BankedReset{}, noResetOfKindError(details, now, choice, usable)
}

// noResetOfKindError explains why the requested kind cannot be spent, and
// which kinds could be, in the spirit of Claude Code's "you can use a …".
func noResetOfKindError(details []decision.BankedReset, now time.Time, choice ResetChoice, usable func(decision.BankedReset) bool) error {
	want := "a reset that refills your " + limitName(choice.Limit)
	if choice.Kind != "" {
		want = describeReset(decision.BankedReset{Kind: choice.Kind})
	}
	reason := ""
	var offers []string
	seen := map[string]bool{}
	for _, d := range details {
		if d.ResetsLeft <= 0 || (d.ExpiresAt != nil && !d.ExpiresAt.After(now)) {
			continue
		}
		if choice.Kind != "" && d.Kind == choice.Kind && reason == "" {
			switch {
			case choice.Limit != "" && !d.RefillsLimit(choice.Limit):
				reason = "doesn't refill your " + limitName(choice.Limit)
			case d.UnavailableReason != "":
				reason = decision.ResetUnavailableText(d.UnavailableReason)
			}
		}
		if usable(d) && (choice.Limit == "" || d.RefillsLimit(choice.Limit)) && d.Kind != "" && !seen[d.Kind] {
			seen[d.Kind] = true
			offers = append(offers, describeReset(d))
		}
	}
	message := "no claimable banked reset is available: " + want
	if reason != "" {
		message += " " + reason
	} else {
		message += " is not available"
	}
	if len(offers) > 0 {
		message += "; you can use " + strings.Join(offers, " or ")
	}
	return errors.New(message)
}

func limitName(limit string) string {
	switch limit {
	case "five_hour":
		return "5-hour limit"
	case "fable":
		return "Fable allowance"
	case "weekly":
		return "weekly limit"
	}
	return "limit"
}

// selectNextBankedReset spends Claude's next grant or Codex's soonest credit.
func selectNextBankedReset(provider string, details []decision.BankedReset, now time.Time) (decision.BankedReset, error) {
	var selected *decision.BankedReset
	if provider == "codex" && len(details) == 0 {
		return decision.BankedReset{}, fmt.Errorf("credit details not loaded yet; try again shortly")
	}
	var held *decision.BankedReset
	for _, detail := range details {
		if detail.ResetsLeft <= 0 || detail.ID == "" || (detail.ExpiresAt != nil && !detail.ExpiresAt.After(now)) {
			continue
		}
		if !detail.Spendable(now) || (provider == "claude" && !detail.Claimable) {
			if held == nil {
				d := detail
				held = &d
			}
			continue
		}
		if selected == nil || (detail.ExpiresAt != nil && (selected.ExpiresAt == nil || detail.ExpiresAt.Before(*selected.ExpiresAt))) {
			d := detail
			selected = &d
		}
	}
	if selected == nil {
		// Say why the resets that are held cannot be spent, when we know.
		if held != nil && held.UnavailableReason != "" {
			return *held, fmt.Errorf("no claimable banked reset is available: %s %s", describeReset(*held), decision.ResetUnavailableText(held.UnavailableReason))
		}
		return decision.BankedReset{}, fmt.Errorf("no claimable banked reset is available")
	}
	if provider == "claude" {
		if selected.CooldownUntil != nil && selected.CooldownUntil.After(now) {
			return *selected, fmt.Errorf("reset is in cooldown until %s", selected.CooldownUntil.Format(time.RFC3339))
		}
		if selected.UsableNow == nil || !*selected.UsableNow {
			if selected.UnavailableReason != "" {
				return *selected, fmt.Errorf("%s %s", describeReset(*selected), decision.ResetUnavailableText(selected.UnavailableReason))
			}
			return *selected, fmt.Errorf("Claude did not report this reset as usable now")
		}
	}
	return *selected, nil
}

// redemptionAccountError refuses spending unless the login the redeemer
// would use is certainly this account's. A Claude Code profile account
// spends from its own profile login. Any other account spends from the
// provider's default login, which is only certain when no other account of
// the provider reads that login too. An OpenUsage card other than the
// default names no local login at all. This also gates reset horizons.
func (s *Server) redemptionAccountError(provider config.Provider) error {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	card := strings.ToLower(strings.TrimSpace(provider.OpenUsageCard))
	if card != "" && card != name {
		return fmt.Errorf("%w: OpenUsage card %s has no local %s login Redline can spend from; "+
			"bind the account to its Claude Code profile (redline account add-profile) or redeem in Claude Code (/limit-reset)",
			store.ErrConflict, card, name)
	}
	if name == "claude" && config.NormalizeClaudeConfigDir(provider.ClaudeConfigDir) != "" {
		return nil
	}
	// SkipBankedResetProviders is set when the default login's reset lookup
	// is ambiguous; it stays a second, conservative signal here.
	if s.defaultLoginAccounts(name) > 1 || s.usageSources.SkipBankedResetProviders[name] {
		return fmt.Errorf("%w: cannot redeem %s resets with multiple accounts on the default login; use the provider CLI", store.ErrConflict, name)
	}
	return nil
}

// defaultLoginAccounts counts configured accounts of a provider that read
// its default login: neither a Claude Code profile nor another OpenUsage
// card.
func (s *Server) defaultLoginAccounts(provider string) int {
	count := 0
	for _, configured := range s.config.Providers {
		if strings.EqualFold(configured.Provider, provider) && config.MeteringIdentity(configured) == "default:"+provider {
			count++
		}
	}
	return count
}

// describeReset names a reset for messages, e.g. "the Fable reset".
func describeReset(reset decision.BankedReset) string {
	switch reset.Kind {
	case "full":
		return "the full reset"
	case "session":
		return "the 5-hour reset"
	case "weekly":
		return "the weekly reset"
	case "fable":
		return "the Fable reset"
	case "opus":
		return "the Opus reset"
	case "sonnet":
		return "the Sonnet reset"
	}
	return "the reset"
}

func (s *Server) RedeemBankedReset(ctx context.Context, providerID, trigger string, dryRun bool) (nativeusage.RedeemResult, error) {
	return s.redeemBankedResetExpected(ctx, providerID, trigger, dryRun, "")
}

// atLimitResetReader is the fresh read used to choose a Claude reset by kind.
type atLimitResetReader interface {
	BankedResetsAtLimit(context.Context, config.Provider) (nativeusage.ClaudeResetsAtLimit, error)
}

// ResetKinds are the values a redemption may ask for: "auto" picks the kind
// that fits the limit the account has reached; the rest name a kind.
var ResetKinds = []string{"auto", "full", "weekly", "session", "fable", "opus", "sonnet"}

func (s *Server) redeemBankedResetExpected(ctx context.Context, providerID, trigger string, dryRun bool, expectedResetID string) (nativeusage.RedeemResult, error) {
	return s.redeemBankedResetKind(ctx, providerID, trigger, dryRun, expectedResetID, "")
}

// resetChoiceFor turns a requested kind into a selection for Claude, reading
// the account's resets fresh at its limit when a kind was requested. Without
// a kind it returns the stored details and no choice: Claude's next grant.
func (s *Server) resetChoiceFor(ctx context.Context, provider config.Provider, kind string, pre decision.UsageSnapshot) ([]decision.BankedReset, ResetChoice, error) {
	if kind == "" {
		return pre.BankedResetDetails, ResetChoice{}, nil
	}
	if !slices.Contains(ResetKinds, kind) {
		return nil, ResetChoice{}, fmt.Errorf("reset kind must be one of %s", strings.Join(ResetKinds, ", "))
	}
	if !strings.EqualFold(strings.TrimSpace(provider.Provider), "claude") {
		return nil, ResetChoice{}, fmt.Errorf("choosing a reset kind is only supported for Claude")
	}
	details := pre.BankedResetDetails
	limit := decision.ReachedLimit(pre)
	if s.atLimitResets != nil {
		if until := s.usageSources.LoginBlockedUntil(provider); s.now().Before(until) {
			return nil, ResetChoice{}, fmt.Errorf("%w: Claude's usage endpoint is rate limited until %s; try again then", store.ErrConflict, until.UTC().Format(time.RFC3339))
		}
		fresh, err := s.atLimitResets.BankedResetsAtLimit(ctx, provider)
		if err != nil {
			s.usageSources.RecordLoginError(provider, err)
			return nil, ResetChoice{}, fmt.Errorf("read Claude resets at the limit: %w", err)
		}
		if fresh.Resets != nil {
			var normalized decision.UsageSnapshot
			normalized.ApplyBankedResets(fresh.Resets)
			details = normalized.BankedResetDetails
		}
		if len(fresh.Exhausted) > 0 {
			limit = fresh.Exhausted[0]
		}
	}
	choice := ResetChoice{Limit: limit}
	if kind != "auto" {
		choice.Kind = kind
	} else if limit == "" {
		return nil, ResetChoice{}, fmt.Errorf("you're not at a usage limit, so there is no reset to pick for it; name a kind instead")
	}
	return details, choice, nil
}

func (s *Server) redeemBankedResetKind(ctx context.Context, providerID, trigger string, dryRun bool, expectedResetID, kind string) (nativeusage.RedeemResult, error) {
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
	if err := s.redemptionAccountError(provider); err != nil {
		return result, err
	}
	// Replay confirmed retries before lockouts unless stored telemetry shows
	// a remaining use after the latest successful redemption of this grant.
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
				if attempt.ResetID != expectedResetID {
					continue
				}
				snapshot, _, err := s.store.LatestSnapshot(ctx, providerID, name)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return result, err
				}
				if !redemptionReflected(attempt, snapshot.BankedResetDetails, snapshot.ObservedAt) {
					return replayedRedemption(attempt), nil
				}
				break // Only the latest successful use can release the guard.
			}
		}
	}
	if until := s.usageSources.LoginBlockedUntil(provider); s.now().Before(until) {
		result.Outcome = "cooldown"
		result.CooldownUntil = &until
		result.Reason = "provider rate-limit lockout is active"
		if !dryRun {
			s.notifyRedemption(ctx, providerID, result, nil)
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
		pre, _, err = s.store.LatestSnapshot(ctx, providerID, name)
		if err != nil {
			return result, err
		}
		if pending.ID != 0 {
			selected.ID = pending.ResetID
			for _, detail := range pre.BankedResetDetails {
				if detail.ID == pending.ResetID {
					selected = detail
					break
				}
			}
		} else {
			details, choice, choiceErr := s.resetChoiceFor(ctx, provider, kind, pre)
			if errors.Is(choiceErr, store.ErrConflict) {
				return result, choiceErr
			}
			if trigger == "automatic" {
				details, err = s.automaticBankedResets(ctx, providerID, name, details, pre.ObservedAt)
				if err != nil {
					return result, err
				}
				// The automatic selector already reserved the provider throttle.
				// Recheck only its candidate against fresh telemetry, not a new reset.
				if expectedResetID != "" {
					var candidate []decision.BankedReset
					for _, detail := range details {
						if detail.ID == expectedResetID {
							candidate = append(candidate, detail)
						}
					}
					details = candidate
				}
			}
			// Every selection path (automatic, confirmed, and unconfirmed such
			// as CLI --yes or direct API calls) applies the completed-use guard,
			// so a lost response or stale telemetry cannot spend a use twice.
			var replay *store.BankedResetRedemption
			{
				completed, lookupErr := s.store.RedeemedBankedResetRedemptions(ctx, providerID)
				if lookupErr != nil {
					return result, lookupErr
				}
				consumed := consumedBankedResets(completed, details, pre.ObservedAt)
				for _, a := range completed {
					if consumed[a.ResetID] && replay == nil {
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
			if choiceErr != nil {
				err = choiceErr
			} else {
				selected, err = SelectBankedResetFor(name, details, s.now(), choice)
			}
		}
		if err != nil {
			result.Outcome = "unavailable"
			result.Reason = err.Error()
			result.ResetID = selected.ID
			result.CooldownUntil = selected.CooldownUntil
			if selected.CooldownUntil != nil && selected.CooldownUntil.After(s.now()) {
				result.Outcome = "cooldown"
			}
			s.notifyRedemption(ctx, providerID, result, nil)
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
			s.notifyRedemption(ctx, providerID, result, nil)
		}
		return result, nil
	}
	newAttempt := store.BankedResetRedemption{ProviderAccountID: providerID, ResetID: req.ResetID, RequestID: req.RequestID, Trigger: trigger, CreatedAt: s.now().UTC()}
	if !dryRun && pending.ID == 0 {
		newAttempt.ResetsLeftBefore = &selected.ResetsLeft
	}
	attempt, err := s.store.BeginBankedResetRedemption(ctx, newAttempt)
	// Deterministic (Codex) keys can collide with a finished attempt. A finished
	// non-redemption must not be overwritten or resent with the key the provider
	// already answered: derive the next generation of the key instead. Each
	// generation is itself deterministic, so a lost response still converges.
	for generation := 1; err == nil && attempt.State == "done" && attempt.Outcome != "redeemed" && pending.ID == 0; generation++ {
		if generation > 1000 {
			return result, fmt.Errorf("too many completed redemption attempts for reset %q", req.ResetID)
		}
		newAttempt.RequestID = uuid.NewSHA1(redemptionGenerationNamespace, []byte(fmt.Sprintf("%s:%d", req.RequestID, generation))).String()
		attempt, err = s.store.BeginBankedResetRedemption(ctx, newAttempt)
	}
	if err != nil {
		return result, err
	}
	if attempt.State == "done" && attempt.Outcome == "redeemed" {
		return replayedRedemption(attempt), nil
	}
	req.RequestID = attempt.RequestID
	result, err = s.redeemer.Redeem(ctx, provider, req)
	result.ResetID, result.RequestID = req.ResetID, req.RequestID
	if len(result.Cleared) == 0 {
		result.Cleared = selected.Clears
	}
	if err != nil {
		// Keep pending on ambiguous transport failures/429s. The next attempt
		// must reuse the persisted request ID, even if the response was lost.
		s.usageSources.RecordLoginError(provider, err)
		result.Outcome = "failed"
		result.Reason = err.Error()
		if recordErr := s.store.RecordBankedResetRedemptionError(context.WithoutCancel(ctx), attempt.ID, result.Reason); recordErr != nil {
			return result, fmt.Errorf("%w (recording redemption failure: %v)", err, recordErr)
		}
		if !dryRun {
			s.notifyRedemption(ctx, providerID, result, nil)
		}
		return result, err
	}
	completedAt := s.now().UTC()
	remaining := make(map[string]string)
	if result.Outcome == "redeemed" && !dryRun {
		s.usageSources.ForgetBankedReset(provider, result.ResetID)
		post, raw, refreshErr := s.usageSources.FetchFresh(ctx, providerID, provider)
		if refreshErr == nil {
			// Recorded against this account, as every collected snapshot
			// is; unattributed, the dashboard would keep showing the
			// account's last snapshot from before the reset.
			post.Account = providerID
			refreshErr = s.store.SaveSnapshot(ctx, post, raw)
		}
		if refreshErr == nil {
			remaining["weekly_remaining"] = strconv.FormatFloat(post.Weekly.Remaining, 'f', -1, 64)
			if post.Short != nil {
				remaining["five_hour_remaining"] = strconv.FormatFloat(post.Short.Remaining, 'f', -1, 64)
			}
		}
		refilled := usageWindowRefilled(pre.Weekly, post.Weekly, 0)
		clearsWeekly := slices.Contains(result.Cleared, "weekly")
		clearsShort := slices.Contains(result.Cleared, "five_hour")
		clearsFable := slices.Contains(result.Cleared, "fable")
		if clearsShort && !clearsWeekly {
			refilled = pre.Short != nil && post.Short != nil && usageWindowRefilled(*pre.Short, *post.Short, 0)
		}
		if clearsFable && !clearsWeekly {
			// A Fable reset refills the Fable allowance and gives back only
			// what Fable used of the week, so any of these shows it applied.
			preFable, hadFable := pre.Allowance("model:fable:weekly")
			postFable, hasFable := post.Allowance("model:fable:weekly")
			refilled = refilled || usageWindowRefilled(pre.Weekly, post.Weekly, 0) ||
				(hadFable && hasFable && postFable.Remaining > preFable.Remaining)
		}
		result.Verified = refreshErr == nil && post.ObservedAt.After(completedAt) && refilled
		if !result.Verified {
			result.Reason = strings.TrimSpace(result.Reason + " Usage has not refilled yet; refresh and check again.")
			if name == "codex" {
				result.Reason += " Codex may still enforce the old window (known provider bug)."
			}
		}
	}
	if err := s.store.CompleteBankedResetRedemption(context.WithoutCancel(ctx), attempt.ID, result.Outcome, result.Code, result.Reason, completedAt); err != nil {
		return result, err
	}
	if !dryRun {
		s.notifyRedemption(ctx, providerID, result, remaining)
	}
	return result, nil
}

// redemptionReflected releases a successful use's consumption guard only when
// telemetry shows a positive, decreased count for the same grant. Missing/zero
// counts and legacy attempts without an observed count remain conservative.
//
// The observation must also postdate the redemption's completion: a lower
// count seen at or before completion may reflect a different, earlier use.
func redemptionReflected(prior store.BankedResetRedemption, details []decision.BankedReset, observedAt time.Time) bool {
	if prior.ResetsLeftBefore == nil || prior.CompletedAt == nil || !observedAt.After(*prior.CompletedAt) {
		return false
	}
	for _, detail := range details {
		if detail.ID == prior.ResetID {
			return detail.ResetsLeft > 0 && detail.ResetsLeft < *prior.ResetsLeftBefore
		}
	}
	return false
}

// completed is newest first; older uses cannot release the latest use's guard.
func consumedBankedResets(completed []store.BankedResetRedemption, details []decision.BankedReset, observedAt time.Time) map[string]bool {
	consumed := make(map[string]bool, len(completed))
	for _, prior := range completed {
		if _, seen := consumed[prior.ResetID]; !seen {
			consumed[prior.ResetID] = !redemptionReflected(prior, details, observedAt)
		}
	}
	return consumed
}

func replayedRedemption(a store.BankedResetRedemption) nativeusage.RedeemResult {
	return nativeusage.RedeemResult{Outcome: a.Outcome, Code: a.Code, Reason: a.Reason, ResetID: a.ResetID, RequestID: a.RequestID, CompletedAt: a.CompletedAt, Replayed: true}
}

func (s *Server) notifyRedemption(ctx context.Context, providerID string, result nativeusage.RedeemResult, remaining map[string]string) {
	if s.notifier == nil || result.Replayed || result.Outcome == "nothing_to_reset" || result.Outcome == "dry_run_ok" {
		return
	}
	event := domain.EventResetRedeemFailed
	if result.Outcome == "redeemed" {
		event = domain.EventResetRedeemed
	}
	data := map[string]string{"reset_id": result.ResetID, "request_id": result.RequestID, "outcome": result.Outcome, "code": result.Code, "reason": result.Reason, "verified": strconv.FormatBool(result.Verified)}
	data["cleared"] = strings.Join(result.Cleared, ",")
	for window, value := range remaining {
		data[window] = value
	}
	if err := s.notifier.Notify(ctx, notification.WithMessage(domain.NotificationEvent{Version: 1, Type: event, OccurredAt: s.now().UTC(), ProviderAccountID: providerID, Data: data}, s.config.Providers[providerID].Provider)); err != nil {
		log.Printf("redline %s redemption notification failed: %v", providerID, err)
	}
}

func (s *Server) redeemBankedReset(w http.ResponseWriter, r *http.Request) {
	// Demo mode never redeems and has no API token, so report that first
	// rather than an authorization error no demo request could pass.
	if s.redemptionDisabled {
		writeError(w, fmt.Errorf("%w: banked reset redemption is disabled in demo mode", store.ErrConflict))
		return
	}
	if p := requestPrincipal(r); !p.owner && (!p.valid() || !s.config.API.PairedDevices.AllowResetRedemption) {
		writeJSON(w, http.StatusForbidden, problem{Error: "paired device reset redemption requires api.paired_devices.allow_reset_redemption: true"})
		return
	}
	var body *struct {
		DryRun          bool   `json:"dry_run"`
		ExpectedResetID string `json:"expected_reset_id,omitempty"`
		Kind            string `json:"kind,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if body == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "request body must be an object"})
		return
	}
	if body.Kind != "" && !slices.Contains(ResetKinds, body.Kind) {
		writeJSON(w, http.StatusBadRequest, problem{Error: "kind must be one of " + strings.Join(ResetKinds, ", ")})
		return
	}
	result, err := s.redeemBankedResetKind(r.Context(), r.PathValue("provider"), "manual", body.DryRun, body.ExpectedResetID, body.Kind)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// bankedResetChoice previews which reset a redemption with this kind would
// spend, without spending it, so a confirmation can name it. Redemption then
// binds to that ID with expected_reset_id. Choosing by kind reads Claude's
// resets fresh, so this is one provider request.
func (s *Server) bankedResetChoice(w http.ResponseWriter, r *http.Request) {
	if p := requestPrincipal(r); !p.owner && (!p.valid() || !s.config.API.PairedDevices.AllowResetRedemption) {
		writeJSON(w, http.StatusForbidden, problem{Error: "paired device reset redemption requires api.paired_devices.allow_reset_redemption: true"})
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := s.config.Providers[providerID]
	if !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !slices.Contains(ResetKinds, kind) {
		writeJSON(w, http.StatusBadRequest, problem{Error: "kind must be one of " + strings.Join(ResetKinds, ", ")})
		return
	}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	// The same account gate as redemption: a preview must not name a reset
	// that redeeming would then refuse, or read another login's resets.
	if err := s.redemptionAccountError(provider); err != nil {
		writeError(w, err)
		return
	}
	// The fresh at-limit read stands on its own when a kind is asked for, so
	// a missing stored snapshot only matters for Claude's plain next grant.
	pre, _, err := s.store.LatestSnapshot(r.Context(), providerID, name)
	if err != nil && (kind == "" || !errors.Is(err, store.ErrNotFound)) {
		writeError(w, err)
		return
	}
	details, choice, err := s.resetChoiceFor(r.Context(), provider, kind, pre)
	if err == nil {
		var selected decision.BankedReset
		if selected, err = SelectBankedResetFor(name, details, s.now(), choice); err == nil {
			writeJSON(w, http.StatusOK, selected)
			return
		}
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusUnprocessableEntity, problem{Error: err.Error()})
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
