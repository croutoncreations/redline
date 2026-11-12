// Package profilelogin keeps the logins of Claude Code profile folders fresh.
//
// Redline reads a profile's login and never writes it: the refresh token
// belongs to that profile's Claude Code. Claude Code renews the 8-hour access
// token only while it runs, so a seat nobody used for 8 hours goes stale. The
// Keeper runs Claude Code under the profile shortly before the token expires,
// and Claude Code renews and saves its own login. Redline still writes
// nothing, and Claude Code's own locking covers a session running at the same
// time.
package profilelogin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/nativeusage"
)

const (
	// DefaultLead renews this long before expiry: about three 5-minute
	// usage-monitor cycles, so one failed attempt still leaves time.
	DefaultLead = 15 * time.Minute
	// DefaultBackoff is the wait after a renewal that did not move the
	// expiry, so a broken profile is not retried every cycle.
	DefaultBackoff = 30 * time.Minute
	// PromptWithin is when the prompt fallback becomes allowed. Claude Code
	// renews a token inside its last few minutes; earlier, a prompt would
	// spend quota without renewing anything, so only auth status is tried.
	PromptWithin = 5 * time.Minute
)

// LoginReader reads one profile's login state without writing it.
type LoginReader interface {
	ProfileLoginState(ctx context.Context, dir string) (nativeusage.ProfileLogin, error)
}

// Renewer runs Claude Code under a profile in one of the renewal modes.
type Renewer interface {
	Renew(ctx context.Context, dir string, mode Mode) error
}

// Mode is how Claude Code is asked to renew: Status first (spends no
// quota); Prompt only when Status did not move the expiry.
type Mode string

const (
	ModeStatus Mode = "auth_status"
	ModePrompt Mode = "prompt"
)

// Outcome of one check.
type Outcome string

const (
	OutcomeFresh     Outcome = "fresh"      // not due yet
	OutcomeWaiting   Outcome = "waiting"    // auth status did not renew yet; the prompt is allowed closer to expiry
	OutcomeRenewed   Outcome = "renewed"    // expiry moved forward
	OutcomeSkipped   Outcome = "skipped"    // busy, backing off, or a run is active
	OutcomeFailed    Outcome = "failed"     // Claude Code ran but the expiry did not move
	OutcomeSignedOut Outcome = "signed_out" // nothing Claude Code can renew; sign in again
)

// Result reports one check.
type Result struct {
	Outcome Outcome
	Mode    Mode // the mode that renewed, when Outcome is renewed
	Before  time.Time
	After   time.Time
	Err     error
}

// Keeper decides when a profile is due and renews it. It is safe for
// concurrent use; only one renewal runs at a time across all profiles.
type Keeper struct {
	Logins  LoginReader
	Renewer Renewer
	Now     func() time.Time
	Lead    time.Duration
	Backoff time.Duration
	// Busy reports a Redline run active on the account: that session
	// renews the login itself, and a second Claude Code is not needed.
	Busy func(ctx context.Context, account string) bool

	renewing sync.Mutex
	mu       sync.Mutex
	// retryAt is per profile directory: the earliest next attempt after a
	// failed renewal.
	retryAt map[string]time.Time
}

func (k *Keeper) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Keeper) lead() time.Duration {
	if k.Lead > 0 {
		return k.Lead
	}
	return DefaultLead
}

func (k *Keeper) backoff() time.Duration {
	if k.Backoff > 0 {
		return k.Backoff
	}
	return DefaultBackoff
}

// Due reports whether the profile's login expires within the lead.
func (k *Keeper) due(state nativeusage.ProfileLogin) bool {
	return !state.ExpiresAt.IsZero() && state.ExpiresAt.Sub(k.now()) <= k.lead()
}

// Check renews the profile at dir when its login is due. force renews even
// outside the lead (the collector just saw the token expire); it still
// respects backoff, a running task, and a renewal already in progress.
func (k *Keeper) Check(ctx context.Context, account, dir string, force bool) Result {
	state, err := k.Logins.ProfileLoginState(ctx, dir)
	if err != nil {
		return Result{Outcome: OutcomeSkipped, Err: err}
	}
	if state.SignedOut || !state.HasRefreshToken {
		return Result{Outcome: OutcomeSignedOut, Before: state.ExpiresAt}
	}
	if state.ExpiresAt.IsZero() || (!force && !k.due(state)) {
		return Result{Outcome: OutcomeFresh, Before: state.ExpiresAt}
	}
	if k.Busy != nil && k.Busy(ctx, account) {
		return Result{Outcome: OutcomeSkipped, Before: state.ExpiresAt}
	}
	k.mu.Lock()
	if retry, ok := k.retryAt[dir]; ok && k.now().Before(retry) {
		k.mu.Unlock()
		return Result{Outcome: OutcomeSkipped, Before: state.ExpiresAt}
	}
	k.mu.Unlock()
	if !k.renewing.TryLock() {
		// Another profile is renewing; this one gets the next cycle.
		return Result{Outcome: OutcomeSkipped, Before: state.ExpiresAt}
	}
	defer k.renewing.Unlock()

	// Status is always tried first: it costs no quota and a few seconds.
	// The prompt only once the token is inside Claude Code's own renewal
	// window, or already expired.
	statusOnly := !force && state.ExpiresAt.Sub(k.now()) > PromptWithin
	modes := []Mode{ModeStatus, ModePrompt}
	if statusOnly {
		modes = modes[:1]
	}
	before := state.ExpiresAt
	var errs []error
	for _, mode := range modes {
		runErr := k.Renewer.Renew(ctx, dir, mode)
		after, readErr := k.Logins.ProfileLoginState(ctx, dir)
		if readErr != nil {
			errs = append(errs, readErr)
			break
		}
		if after.ExpiresAt.After(before) {
			k.mu.Lock()
			delete(k.retryAt, dir)
			k.mu.Unlock()
			log.Printf("redline profile_login %s renewed via %s: expiry %s -> %s", account, mode,
				before.Local().Format(time.TimeOnly), after.ExpiresAt.Local().Format(time.TimeOnly))
			return Result{Outcome: OutcomeRenewed, Mode: mode, Before: before, After: after.ExpiresAt}
		}
		if after.SignedOut || !after.HasRefreshToken {
			return Result{Outcome: OutcomeSignedOut, Before: before}
		}
		if runErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", mode, runErr))
		}
		if ctx.Err() != nil {
			break
		}
	}
	if statusOnly && ctx.Err() == nil {
		// Not a failure: the next cycle, closer to expiry, may renew or
		// fall back to the prompt.
		return Result{Outcome: OutcomeWaiting, Before: before, Err: errors.Join(errs...)}
	}
	k.mu.Lock()
	if k.retryAt == nil {
		k.retryAt = map[string]time.Time{}
	}
	k.retryAt[dir] = k.now().Add(k.backoff())
	k.mu.Unlock()
	err = errors.Join(errs...)
	if err == nil {
		err = errors.New("Claude Code ran but the login's expiry did not move")
	}
	log.Printf("redline profile_login %s renewal failed (retry in %s): %v", account, k.backoff(), err)
	return Result{Outcome: OutcomeFailed, Before: before, Err: err}
}
