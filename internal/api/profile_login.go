package api

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/profilelogin"
)

// profileLogins keeps Claude Code profile logins fresh (see package
// profilelogin) and remembers which accounts were already notified as
// needing a sign-in, so one sign-out sends one notification.
type profileLogins struct {
	keeper *profilelogin.Keeper

	mu       sync.Mutex
	notified map[string]bool
}

func newProfileLogins(cfg config.Config, logins profilelogin.LoginReader, renewer profilelogin.Renewer, busy func(context.Context, string) bool, now func() time.Time) *profileLogins {
	if !cfg.KeepProfileLoginsFresh() {
		return nil
	}
	return &profileLogins{
		keeper:   &profilelogin.Keeper{Logins: logins, Renewer: renewer, Now: now, Busy: busy},
		notified: map[string]bool{},
	}
}

// profileDir is the account's Claude Code profile directory, or "" for any
// account the keep-alive must not touch: the default login (OpenUsage or a
// running Claude Code keeps it fresh) and every other provider.
func profileDir(provider config.Provider) string {
	if !strings.EqualFold(provider.Provider, "claude") {
		return ""
	}
	return config.NormalizeClaudeConfigDir(provider.ClaudeConfigDir)
}

// keepProfileLoginFresh renews the account's profile login when it is due.
// force renews now: the collector just found the token expired.
func (s *Server) keepProfileLoginFresh(ctx context.Context, providerID string, force bool) profilelogin.Result {
	configured, ok := s.config.Providers[providerID]
	dir := profileDir(configured)
	if s.profileLogins == nil || !ok || dir == "" {
		return profilelogin.Result{Outcome: profilelogin.OutcomeFresh}
	}
	result := s.profileLogins.keeper.Check(ctx, providerID, dir, force)
	s.profileLogins.noteOutcome(ctx, s, providerID, configured, result)
	return result
}

// noteOutcome sends account.needs_login once per sign-out (per service
// start: the memory is not persisted, so a restart repeats it once), and
// forgets it once the login works again so a later sign-out notifies again.
func (p *profileLogins) noteOutcome(ctx context.Context, s *Server, providerID string, configured config.Provider, result profilelogin.Result) {
	p.mu.Lock()
	switch {
	case result.NeedsLogin():
		if p.notified[providerID] {
			p.mu.Unlock()
			return
		}
		p.notified[providerID] = true
	case result.Outcome == profilelogin.OutcomeFresh || result.Outcome == profilelogin.OutcomeRenewed:
		delete(p.notified, providerID)
		p.mu.Unlock()
		return
	default:
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	if s.notifier == nil {
		return
	}
	// (notified is set before delivery so concurrent checks don't both send;
	// a failed delivery clears it below.)
	label := configured.Label
	if label == "" {
		label = providerID
	}
	event := notification.WithMessage(domain.NotificationEvent{
		Version: 1, Type: domain.EventAccountNeedsLogin, OccurredAt: s.now().UTC(), ProviderAccountID: providerID,
		Data: map[string]string{"label": label, "claude_config_dir": config.DisplayClaudeConfigDir(configured.ClaudeConfigDir)},
	}, configured.Provider)
	if err := s.notifier.Notify(ctx, event); err != nil {
		// Not delivered: allow the next check to try again.
		p.mu.Lock()
		delete(p.notified, providerID)
		p.mu.Unlock()
		log.Printf("redline profile_login %s needs-login notification failed: %v", providerID, err)
	}
}

// renewIfExpired is the collector's other half: a fetch that failed because
// the profile's token expired renews it immediately and fetches once more,
// so a seat that lapsed between cycles recovers in the same cycle.
func (s *Server) renewIfExpired(ctx context.Context, providerID string, fetchErr error) bool {
	if !nativeusage.IsProfileNeedsSession(fetchErr) {
		return false
	}
	return s.keepProfileLoginFresh(ctx, providerID, true).Outcome == profilelogin.OutcomeRenewed
}
