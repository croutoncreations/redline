// Package usage selects exactly one allowance source for each provider account.
package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/openusage"
)

type Source interface {
	Name() string
	Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error)
}

// BankedResetSource supplies supplementary quota reset reports: Claude grants
// or Codex credit details not carried by the primary usage source.
type BankedResetSource interface {
	BankedResets(context.Context, config.Provider) (*decision.BankedResets, error)
}

type bankedResetState struct {
	resets    *decision.BankedResets
	known     bool
	fetchedAt time.Time
	nextFetch time.Time
	inFlight  bool
	lastError string
}

// Anthropic's usage endpoint rate-limits per account, shared with Claude Code
// and every other local usage widget, and extends the penalty when asked
// again during it (seen growing from 462s to 3600s). Resets change a few
// times a month, so ask rarely and back off hard.
const (
	defaultBankedResetInterval = 30 * time.Minute
	// Floor after any failure, including a 429 that says "retry-after: 0".
	bankedResetFailureBackoff = time.Hour
)

type Status struct {
	Active    string    `json:"active"`
	ChangedAt time.Time `json:"changed_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	// Reason names a failure a person can fix, when the source knows one
	// (ReasonSignedOut), and is empty otherwise. LastError has the words.
	Reason   string `json:"reason,omitempty"`
	Failures int    `json:"consecutive_failures"`
	// BankedResetsError explains why the supplementary reset lookup is
	// failing. It never affects scheduling.
	BankedResetsError string `json:"banked_resets_error,omitempty"`
}

// ReasonSignedOut: the provider's local login is gone and only signing in
// again fixes it.
const ReasonSignedOut = "signed_out"

// failureReason returns the fixable cause an error declares, if any.
func failureReason(err error) string {
	var reasoned interface{ Reason() string }
	if errors.As(err, &reasoned) {
		return reasoned.Reason()
	}
	return ""
}

type sourceState struct {
	Status
	nextProbe time.Time
}

type Manager struct {
	OpenUsage       Source
	Native          Source
	FailureLimit    int
	ReprobeInterval time.Duration
	MaxSnapshotAge  time.Duration
	Now             func() time.Time
	// BankedResets, when set, fills in missing Claude resets or Codex details.
	// It is polled at most every BankedResetInterval
	// and its last good answer is reused in between, and while it fails.
	BankedResets        BankedResetSource
	BankedResetInterval time.Duration
	mu                  sync.Mutex
	states              map[string]sourceState
	resetStates         map[string]bankedResetState
	// nativeBlockedUntil holds, per provider, when a native request may
	// next be sent after the provider rate-limited us. Asking early during
	// Anthropic's usage lockout lengthens it.
	nativeBlockedUntil map[string]time.Time
	lockoutPath        string
	accountProviders   map[string]string
	// SkipBankedResetProviders disables lookups by provider type when its
	// single local login may not be the account being monitored.
	SkipBankedResetProviders map[string]bool
	// lookups tracks background lookups so tests can wait for them.
	lookups sync.WaitGroup
}

func NewManager(openUsage, native Source, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	manager := &Manager{OpenUsage: openUsage, Native: native, FailureLimit: 2,
		ReprobeInterval: 15 * time.Minute, Now: now, states: make(map[string]sourceState)}
	if resets, ok := native.(BankedResetSource); ok {
		manager.BankedResets = resets
	}
	return manager
}

func (m *Manager) Fetch(ctx context.Context, accountID string, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	snapshot, raw, err := m.fetchUsage(ctx, accountID, provider)
	if err == nil {
		m.supplementBankedResets(ctx, accountID, provider, &snapshot)
	}
	return snapshot, raw, err
}

// FetchFresh bypasses OpenUsage and supplementary caches for post-redemption
// verification, while still honoring the provider's native rate-limit lockout.
func (m *Manager) FetchFresh(ctx context.Context, accountID string, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	if m.Native == nil {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("native usage source is unavailable")
	}
	return m.fetch(ctx, accountID, provider, m.Native, true)
}

// supplementBankedResets adds missing reset reports or details to a snapshot.
// It never fails the fetch: resets are
// informational, and a rate-limited or ineligible lookup must not cost the
// scheduler its usage data.
func (m *Manager) supplementBankedResets(ctx context.Context, accountID string, provider config.Provider, snapshot *decision.UsageSnapshot) {
	// An account pinned to OpenUsage opted out of native credential access.
	// Native Claude snapshots already asked for grants; Codex usage carries
	// only a count and needs the separate credit-details endpoint.
	if m.BankedResets == nil || m.SkipBankedResetProviders[providerKey(snapshot.Provider)] || provider.EffectiveUsageSource() == "openusage" {
		return
	}
	// The native lookup reads the default login on this Mac. An account bound
	// to another OpenUsage card is a different login, and showing the default
	// login's resets on it would be wrong; OpenUsage reports that card's own
	// resets instead.
	if card := strings.ToLower(strings.TrimSpace(provider.OpenUsageCard)); card != "" && card != providerKey(provider.Provider) {
		return
	}
	// A Claude Code profile is its own login; the native fetch already read
	// its resets, and the default login's would be another account's.
	if strings.TrimSpace(provider.ClaudeConfigDir) != "" {
		return
	}
	codexDetails := snapshot.Provider == "codex" && snapshot.BankedResets != nil && *snapshot.BankedResets > 0 && len(snapshot.BankedResetDetails) == 0
	claudeReport := snapshot.Provider == "claude" && snapshot.BankedResets == nil && snapshot.Source != "native"
	if !codexDetails && !claudeReport {
		return
	}
	// The native lookup reads the single local provider login no matter which
	// account asked, so the answer is cached per provider credential rather
	// than per account: one request serves every account, and none of them
	// multiplies the load on a rate-limited endpoint.
	key := providerKey(snapshot.Provider)
	now := m.Now().UTC()
	m.mu.Lock()
	if m.resetStates == nil {
		m.resetStates = make(map[string]bankedResetState)
		m.accountProviders = make(map[string]string)
	}
	m.accountProviders[accountID] = key
	state := m.resetStates[key]
	if !state.inFlight && !now.Before(state.nextFetch) && !now.Before(m.nativeBlockedUntil[key]) {
		// Claim the slot before unlocking so concurrent fetches do not all
		// fire the same lookup, then look up in the background: resets are
		// informational and must not slow a scheduler cycle or /refresh.
		// This snapshot uses the cached answer; the next one gets the new.
		state.inFlight = true
		m.resetStates[key] = state
		m.lookups.Add(1)
		go m.lookupBankedResets(context.WithoutCancel(ctx), key, provider)
	}
	m.mu.Unlock()

	// A cached answer is only trusted for a day; after that, say nothing
	// rather than show a count that may already have been spent.
	if state.known && now.Sub(state.fetchedAt) <= 24*time.Hour {
		report := state.resets
		if report != nil && report.NextExpiresAt != nil && !report.NextExpiresAt.After(now) {
			if len(report.Details) == 0 {
				// Without details we cannot tell how many resets remain.
				report = nil
			} else {
				filtered := *report
				filtered.Details = nil
				filtered.NextExpiresAt = nil
				for _, detail := range report.Details {
					if detail.ExpiresAt != nil && !detail.ExpiresAt.After(now) {
						filtered.Available = max(0, filtered.Available-detail.ResetsLeft)
						continue
					}
					filtered.Details = append(filtered.Details, detail)
					if detail.ExpiresAt != nil && (filtered.NextExpiresAt == nil || detail.ExpiresAt.Before(*filtered.NextExpiresAt)) {
						filtered.NextExpiresAt = detail.ExpiresAt
					}
				}
				report = &filtered
			}
		}
		if codexDetails {
			if report != nil {
				// The primary source's count and expiry remain authoritative.
				expiry := snapshot.BankedResetsExpireAt
				snapshot.ApplyBankedResets(&decision.BankedResets{Available: *snapshot.BankedResets,
					NextExpiresAt: report.NextExpiresAt, Expiries: snapshot.BankedResetsExpiries, Details: report.Details})
				if expiry != nil {
					snapshot.BankedResetsExpireAt = expiry
				}
			}
		} else {
			snapshot.ApplyBankedResets(report)
		}
	}
}

func (m *Manager) lookupBankedResets(ctx context.Context, key string, provider config.Provider) {
	defer m.lookups.Done()
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resets, err := m.BankedResets.BankedResets(lookupCtx, provider)
	cancel()
	fetchedAt := m.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.resetStates[key]
	state.inFlight = false
	if err != nil {
		state.lastError = redact(err.Error())
		backoff := max(m.bankedResetInterval(key), bankedResetFailureBackoff)
		var limited interface{ RetryAfter() time.Duration }
		if errors.As(err, &limited) && limited.RetryAfter() > backoff {
			backoff = limited.RetryAfter()
		}
		state.nextFetch = fetchedAt.Add(backoff)
	} else {
		state.resets, state.known, state.fetchedAt, state.lastError = resets, true, fetchedAt, ""
		state.nextFetch = fetchedAt.Add(m.bankedResetInterval(key))
	}
	m.resetStates[key] = state
	// Claude's report comes from the same endpoint as native usage, so its rate
	// limit is shared. Codex credit details are a separate, per-IP limited
	// endpoint: a 429 there backs off only further detail lookups (nextFetch
	// above) and must not stop wham/usage collection.
	if wait, limited := rateLimitWait(err); limited && key != "codex" {
		if m.nativeBlockedUntil == nil {
			m.nativeBlockedUntil = make(map[string]time.Time)
		}
		if until := fetchedAt.Add(wait); until.After(m.nativeBlockedUntil[key]) {
			m.nativeBlockedUntil[key] = until
		}
	}
	m.saveLockoutsLocked()
}

// ForgetBankedReset takes one use of a just-redeemed reset out of the cached
// report and asks for a fresh report on the next fetch. Until then the cache
// would still offer the spent credit, and the next redemption would select
// it instead of one that is still available.
func (m *Manager) ForgetBankedReset(provider config.Provider, resetID string) {
	key := providerKey(provider.Provider)
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.resetStates[key]
	if !ok {
		return
	}
	if state.resets != nil {
		report := *state.resets
		report.Details, report.NextExpiresAt = nil, nil
		for _, detail := range state.resets.Details {
			if detail.ID == resetID {
				detail.ResetsLeft--
				report.Available = max(0, report.Available-1)
				if detail.ResetsLeft <= 0 {
					continue
				}
			}
			report.Details = append(report.Details, detail)
			if detail.ExpiresAt != nil && (report.NextExpiresAt == nil || detail.ExpiresAt.Before(*report.NextExpiresAt)) {
				report.NextExpiresAt = detail.ExpiresAt
			}
		}
		state.resets = &report
	}
	if !state.inFlight {
		state.nextFetch = time.Time{}
	}
	m.resetStates[key] = state
}

// WaitForBankedResetLookups blocks until background reset lookups finish.
func (m *Manager) WaitForBankedResetLookups() { m.lookups.Wait() }

func (m *Manager) bankedResetInterval(provider string) time.Duration {
	if m.BankedResetInterval <= 0 || (provider == "codex" && m.BankedResetInterval < defaultBankedResetInterval) {
		return defaultBankedResetInterval
	}
	return m.BankedResetInterval
}

func (m *Manager) fetchUsage(ctx context.Context, accountID string, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	mode := provider.EffectiveUsageSource()
	if mode == "openusage" || readsNonDefaultCard(provider) {
		// An account bound to a non-default OpenUsage card never falls back
		// to the native collector: that reads the default login on this Mac,
		// which is a different account, and its numbers would be shown and
		// scheduled against under this account's name.
		return m.fetch(ctx, accountID, provider, m.OpenUsage, false)
	}
	if mode == "native" {
		return m.fetch(ctx, accountID, provider, m.Native, true)
	}

	m.mu.Lock()
	state := m.states[accountID]
	if state.Active == "" {
		state.Active = "openusage"
	}
	now := m.Now().UTC()
	active := state.Active
	shouldProbe := active == "native" && !now.Before(state.nextProbe)
	m.mu.Unlock()

	if active == "openusage" || shouldProbe {
		snapshot, raw, err := m.OpenUsage.Fetch(ctx, provider)
		fetchedAt := m.Now().UTC()
		freshnessFailure := false
		if err == nil {
			err = m.snapshotError(snapshot, fetchedAt)
			freshnessFailure = err != nil
		}
		if err == nil {
			m.success(accountID, "openusage", fetchedAt)
			return snapshot, raw, nil
		}
		if active == "openusage" {
			failures := m.failure(accountID, err)
			if !freshnessFailure && failures < m.failureLimit() {
				return decision.UsageSnapshot{}, nil, fmt.Errorf("openusage usage source: %w", err)
			}
		} else {
			m.probeFailure(accountID, err, fetchedAt)
		}
	}

	snapshot, raw, err := m.fetchNative(ctx, provider)
	fetchedAt := m.Now().UTC()
	if err == nil {
		err = m.snapshotError(snapshot, fetchedAt)
	}
	if err != nil {
		m.nativeFailure(accountID, err)
		return decision.UsageSnapshot{}, nil, fmt.Errorf("native usage source: %w", err)
	}
	m.success(accountID, "native", fetchedAt)
	if active != "native" {
		m.mu.Lock()
		state = m.states[accountID]
		state.nextProbe = fetchedAt.Add(m.reprobeInterval())
		m.states[accountID] = state
		m.mu.Unlock()
	}
	return snapshot, raw, nil
}

func (m *Manager) Status(accountID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.states[accountID].Status
	if state, ok := m.resetStates[m.accountProviders[accountID]]; ok {
		status.BankedResetsError = state.lastError
	}
	return status
}

// fetchNative calls the native source unless the provider is still inside a
// rate-limit penalty, in which case it fails without sending anything.
func (m *Manager) fetchNative(ctx context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	key := nativeLoginKey(provider)
	now := m.Now().UTC()
	m.mu.Lock()
	until := m.nativeBlockedUntil[key]
	m.mu.Unlock()
	if now.Before(until) {
		return decision.UsageSnapshot{}, nil, fmt.Errorf("rate limited by provider; next request after %s", until.Format(time.RFC3339))
	}
	snapshot, raw, err := m.Native.Fetch(ctx, provider)
	m.RecordNativeError(key, err)
	return snapshot, raw, err
}

// NativeBlockedUntil shares the native usage lockout with reset redemptions.
func (m *Manager) NativeBlockedUntil(provider string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nativeBlockedUntil[providerKey(provider)]
}

// RecordNativeError applies the same persisted rate-limit backoff to a redeem
// response as to a usage response.
func (m *Manager) RecordNativeError(provider string, err error) {
	if wait, limited := rateLimitWait(err); limited {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.nativeBlockedUntil == nil {
			m.nativeBlockedUntil = make(map[string]time.Time)
		}
		key := providerKey(provider)
		if until := m.Now().UTC().Add(wait); until.After(m.nativeBlockedUntil[key]) {
			m.nativeBlockedUntil[key] = until
		}
		m.saveLockoutsLocked()
	}
}

// rateLimitWait reports whether err is a provider rate limit and how long to
// stay quiet: the server's Retry-After, but never less than the floor, since
// Anthropic has been seen answering "retry-after: 0" while still limiting.
func rateLimitWait(err error) (time.Duration, bool) {
	var limited interface {
		RetryAfter() time.Duration
		RateLimited() bool
	}
	if !errors.As(err, &limited) || !limited.RateLimited() {
		return 0, false
	}
	return max(limited.RetryAfter(), rateLimitFloor), true
}

const rateLimitFloor = 15 * time.Minute

// readsNonDefaultCard reports an account bound to an OpenUsage card other
// than the provider's default one.
func readsNonDefaultCard(provider config.Provider) bool {
	card := strings.ToLower(strings.TrimSpace(provider.OpenUsageCard))
	return card != "" && card != providerKey(provider.Provider)
}

// providerKey is the one spelling of a provider used for every lockout, so a
// rate limit seen by the native fetch and by the reset lookup is shared even
// when the config writes the provider as "Claude".
func providerKey(provider string) string { return strings.ToLower(strings.TrimSpace(provider)) }

// nativeLoginKey names the login a native fetch uses, for its lockout. The
// default login keeps the provider key, so it still shares one lockout with
// the reset lookup (which only reads the default login); each Claude Code
// profile directory is a separate login with its own token, and one
// profile being rate limited must not silence the others.
func nativeLoginKey(provider config.Provider) string {
	key := providerKey(provider.Provider)
	if dir := config.NormalizeClaudeConfigDir(provider.ClaudeConfigDir); dir != "" && key == "claude" {
		return key + "@profile:" + dir
	}
	return key
}

// fetch reads one pinned source. native is passed explicitly rather than
// derived from source == m.Native: comparing interfaces panics when the
// dynamic type is uncomparable (e.g. a struct holding a map), and the same
// value may legitimately back both sources.
func (m *Manager) fetch(ctx context.Context, accountID string, provider config.Provider, source Source, native bool) (decision.UsageSnapshot, []byte, error) {
	var snapshot decision.UsageSnapshot
	var raw []byte
	var err error
	if native {
		snapshot, raw, err = m.fetchNative(ctx, provider)
	} else {
		snapshot, raw, err = source.Fetch(ctx, provider)
	}
	now := m.Now().UTC()
	if err == nil {
		err = m.snapshotError(snapshot, now)
	}
	if err != nil {
		m.nativeFailure(accountID, err)
		return decision.UsageSnapshot{}, nil, fmt.Errorf("%s usage source: %w", source.Name(), err)
	}
	m.success(accountID, source.Name(), now)
	return snapshot, raw, nil
}

func (m *Manager) snapshotError(snapshot decision.UsageSnapshot, now time.Time) error {
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("usage snapshot has no observation timestamp")
	}
	if snapshot.ObservedAt.After(now) {
		return fmt.Errorf("usage snapshot is from the future")
	}
	if now.Sub(snapshot.ObservedAt) > m.maxSnapshotAge() {
		return fmt.Errorf("usage snapshot is stale")
	}
	return nil
}

func (m *Manager) success(accountID, active string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	preserveFallbackError := active == "native" && state.Active == "openusage"
	if state.Active != active {
		state.ChangedAt = now
	}
	state.Active, state.Failures = active, 0
	if !preserveFallbackError {
		state.LastError, state.Reason = "", ""
	}
	m.states[accountID] = state
}

func (m *Manager) failure(accountID string, err error) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.Failures++
	state.LastError, state.Reason = redact(err.Error()), failureReason(err)
	if state.Active == "" {
		state.Active = "openusage"
	}
	m.states[accountID] = state
	return state.Failures
}

func (m *Manager) probeFailure(accountID string, err error, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.LastError, state.Reason = redact(err.Error()), failureReason(err)
	state.nextProbe = now.Add(m.reprobeInterval())
	m.states[accountID] = state
}

func (m *Manager) nativeFailure(accountID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.states[accountID]
	state.LastError, state.Reason = redact(err.Error()), failureReason(err)
	m.states[accountID] = state
}

func (m *Manager) failureLimit() int {
	if m.FailureLimit <= 0 {
		return 2
	}
	return m.FailureLimit
}

func (m *Manager) reprobeInterval() time.Duration {
	if m.ReprobeInterval <= 0 {
		return 15 * time.Minute
	}
	return m.ReprobeInterval
}

func (m *Manager) maxSnapshotAge() time.Duration {
	if m.MaxSnapshotAge <= 0 {
		return 15 * time.Minute
	}
	return m.MaxSnapshotAge
}

// redact keeps account identities out of collection errors, which reach the
// dashboard, the phone and the relay. Collectors should never put an email
// in an error, but upstream text (OpenUsage card errors) can.
func redact(text string) string { return openusage.RedactEmails(text) }
