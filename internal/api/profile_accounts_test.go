package api

import (
	"context"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/store"
)

// profileAccountsServer is a team of Claude Code profile accounts: "main"
// reads ~/.claude-cc-jon and "frank" its own profile. Neither reads the
// default login.
func profileAccountsServer(t *testing.T) (*Server, *store.DB, decision.UsageSnapshot) {
	t.Helper()
	s, db, pre := redemptionServer(t, "claude")
	main := s.config.Providers["main"]
	main.ClaudeConfigDir = "/p/.claude-cc-jon"
	s.config.Providers["main"] = main
	s.config.Providers["frank"] = config.Provider{Provider: "claude", UsageSource: "native", ClaudeConfigDir: "/p/.claude-cc-frank"}
	return s, db, pre
}

// Several profile accounts may each redeem: every one spends from its own
// login, and the redeemer is handed that account's profile.
func TestProfileAccountRedeemsFromItsOwnLogin(t *testing.T) {
	s, _, _ := profileAccountsServer(t)
	var spentFrom []string
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "key"}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		return nativeusage.RedeemResult{Outcome: "redeemed", Code: "reset"}, nil
	}}
	s.redeemer = recordingRedeemer{bankedResetRedeemer: s.redeemer, spentFrom: &spentFrom}
	if _, err := s.RedeemBankedReset(t.Context(), "main", "manual", false); err != nil {
		t.Fatal(err)
	}
	if len(spentFrom) != 1 || spentFrom[0] != "/p/.claude-cc-jon" {
		t.Fatalf("redeemed from %v", spentFrom)
	}
}

type recordingRedeemer struct {
	bankedResetRedeemer
	spentFrom *[]string
}

func (r recordingRedeemer) Redeem(ctx context.Context, provider config.Provider, req nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
	*r.spentFrom = append(*r.spentFrom, provider.ClaudeConfigDir)
	return r.bankedResetRedeemer.Redeem(ctx, provider, req)
}

// A rate limit on one profile's login holds that profile's redemptions and
// at-limit reads only; another profile and the default login carry on.
func TestOneProfileLockoutDoesNotHoldAnotherProfilesRedemption(t *testing.T) {
	s, db, pre := profileAccountsServer(t)
	s.usageSources.RecordLoginError(s.config.Providers["main"], redemptionRateLimit{})
	got, err := s.RedeemBankedReset(t.Context(), "main", "manual", false)
	if err != nil || got.Outcome != "cooldown" {
		t.Fatalf("limited profile: %+v %v", got, err)
	}
	if until := s.usageSources.LoginBlockedUntil(s.config.Providers["frank"]); !until.IsZero() {
		t.Fatalf("another profile was locked out until %v", until)
	}
	frankSnapshot := pre
	frankSnapshot.Account = "frank"
	if err := db.SaveSnapshot(t.Context(), frankSnapshot, nil); err != nil {
		t.Fatal(err)
	}
	s.redeemer = testRedeemer{prepare: func() nativeusage.RedeemRequest {
		return nativeusage.RedeemRequest{ResetID: "reset", RequestID: "frank-key"}
	}, redeem: func(nativeusage.RedeemRequest) (nativeusage.RedeemResult, error) {
		return nativeusage.RedeemResult{Outcome: "redeemed", Code: "reset"}, nil
	}}
	if got, err := s.RedeemBankedReset(t.Context(), "frank", "manual", false); err != nil || got.Outcome != "redeemed" {
		t.Fatalf("another profile was held: %+v %v", got, err)
	}
	if until := s.usageSources.NativeBlockedUntil("claude"); !until.IsZero() {
		t.Fatalf("the default login was locked out until %v", until)
	}
	reads := &fakeAtLimit{}
	s.atLimitResets = reads
	if _, _, err := s.resetChoiceFor(t.Context(), s.config.Providers["main"], "auto", decision.UsageSnapshot{}); err == nil ||
		!strings.Contains(err.Error(), "rate limited") || reads.calls != 0 {
		t.Fatalf("at-limit read inside the profile's lockout: calls=%d err=%v", reads.calls, err)
	}
}

// An OpenUsage card other than the default names no local login. Its
// numbers are shown, but no work is dispatched to it (a run would start as
// the default login) and no reset is redeemed for it.
func TestOpenUsageCardAccountShowsUsageButHoldsWork(t *testing.T) {
	s, _, _ := redemptionServer(t, "claude")
	card := s.config.Providers["main"]
	card.UsageSource, card.OpenUsageCard = "openusage", "claude@f48a43e1"
	s.config.Providers["main"] = card
	response, admitted, err := s.dispatchCore(t.Context(), schedulerRequest{ProviderAccountID: "main"}, "automatic")
	if err != nil || admitted {
		t.Fatalf("admitted=%v err=%v", admitted, err)
	}
	if response.Result.Decision != decision.Wait || response.Result.Mode != decision.ModeLoginUnknown ||
		!strings.Contains(response.Result.Reason, "add-profile") {
		t.Fatalf("result = %+v", response.Result)
	}
	if _, err := s.RedeemBankedReset(t.Context(), "main", "manual", false); err == nil || !strings.Contains(err.Error(), "no local claude login") {
		t.Fatalf("redeem err = %v", err)
	}
	// A profile account beside it is unaffected.
	s.config.Providers["frank"] = config.Provider{Provider: "claude", UsageSource: "native", ClaudeConfigDir: "/p/.claude-cc-frank"}
	if reason := config.RunLoginUnknown(s.config.Providers["frank"]); reason != "" {
		t.Fatal(reason)
	}
}
