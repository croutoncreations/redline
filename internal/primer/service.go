package primer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

const (
	TickInterval = 30 * time.Second
	// verifyAfter leaves the provider time to report the new window.
	verifyAfter        = 90 * time.Second
	verifyRefreshEvery = 3 * time.Minute
	verifyGiveUp       = 30 * time.Minute
	// windowSlack allows for the provider rounding a window's start down.
	windowSlack = 11 * time.Minute
	// failureBackoff caps how often reset mode retries after failed pings
	// (signed out, CLI missing) so it neither spawns Claude nor notifies
	// every idle bucket.
	failureBackoffMax = 4 * time.Hour
	// retention bounds the attempt history; pruning runs once a day.
	retention     = 90 * 24 * time.Hour
	pruneInterval = 24 * time.Hour
)

// Store is the persistence the primer needs.
type Store interface {
	PrimerSettings(context.Context, string) (domain.PrimerSettings, error)
	SavePrimerSettings(context.Context, domain.PrimerSettings) error
	ClaimPrimerSlot(context.Context, domain.PrimerAttempt) (int64, error)
	FinishPrimerAttempt(context.Context, domain.PrimerAttempt) error
	VerifyPrimerAttempt(context.Context, int64, domain.PrimerVerification, *time.Time, string, time.Time) error
	ListPrimerAttempts(context.Context, string, int) ([]domain.PrimerAttempt, error)
	PendingPrimerVerifications(context.Context, string) ([]domain.PrimerAttempt, error)
	FailInterruptedPrimerAttempts(context.Context, time.Time) error
	PrunePrimerAttempts(context.Context, time.Time) error
	ActiveRunCount(context.Context, string) (int, error)
}

// Service decides when to ping, sends pings, and checks they opened a window.
type Service struct {
	Store  Store
	Pinger Pinger
	// Providers maps account IDs to provider kinds such as "claude".
	Providers map[string]string
	// Latest returns the newest stored snapshot, or store.ErrNotFound.
	Latest func(context.Context, string) (decision.UsageSnapshot, error)
	// Refresh fetches and stores a fresh snapshot.
	Refresh func(context.Context, string) (decision.UsageSnapshot, error)
	Notify  func(context.Context, domain.NotificationEvent)
	Now     func() time.Time

	mu            sync.Mutex
	settingsMu    sync.Mutex
	lastCheck     map[string]time.Time
	inFlight      map[string]bool
	verifyFetched map[int64]time.Time
}

// Supported reports whether the primer can drive a provider kind.
func Supported(kind string) bool { return kind == "claude" }

// ErrBusy means a ping for the provider is already in progress.
var ErrBusy = errors.New("a ping is already in progress for this provider")

// ErrUnsupported means the provider kind cannot be primed, or several
// accounts share the one local login so pings could not be attributed.
var ErrUnsupported = errors.New("the window primer is unsupported for this provider")

// supported reports whether a configured account can be primed. The Claude
// CLI uses the one local login, so with several Claude accounts a ping could
// open any of their windows and verification would check the wrong one.
func (s *Service) supported(provider string) (bool, string) {
	kind, ok := s.Providers[provider]
	if !ok || !Supported(kind) {
		return false, "the window primer supports Claude accounts only"
	}
	count := 0
	for _, other := range s.Providers {
		if other == kind {
			count++
		}
	}
	if count > 1 {
		return false, "several Claude accounts share this machine's one Claude Code login, so pings cannot be attributed to an account"
	}
	return true, ""
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Recover marks attempts a previous process left running as failed. Call it
// before serving requests, so a ping started by this process is never
// mistaken for an interrupted one.
func (s *Service) Recover(ctx context.Context) error {
	return s.Store.FailInterruptedPrimerAttempts(ctx, s.now())
}

// Run ticks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.prune(ctx)
	s.Tick(ctx)
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	pruner := time.NewTicker(pruneInterval)
	defer pruner.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		case <-pruner.C:
			s.prune(ctx)
		}
	}
}

func (s *Service) prune(ctx context.Context) {
	if err := s.Store.PrunePrimerAttempts(ctx, s.now().Add(-retention)); err != nil {
		log.Printf("window primer: %v", err)
	}
}

// Settings returns saved settings or the defaults.
func (s *Service) Settings(ctx context.Context, provider string) (domain.PrimerSettings, bool, error) {
	settings, err := s.Store.PrimerSettings(ctx, provider)
	if errors.Is(err, store.ErrNotFound) {
		return Defaults(provider), false, nil
	}
	return settings, err == nil, err
}

// Save validates and stores settings.
func (s *Service) Save(ctx context.Context, settings domain.PrimerSettings) (domain.PrimerSettings, error) {
	return s.Update(ctx, settings.ProviderAccountID, func(current *domain.PrimerSettings) {
		settings.ProviderAccountID = current.ProviderAccountID
		*current = settings
	})
}

// Update applies a change to the stored settings under the service lock, so
// concurrent partial updates cannot lose each other's fields.
func (s *Service) Update(ctx context.Context, provider string, change func(*domain.PrimerSettings)) (domain.PrimerSettings, error) {
	if _, ok := s.Providers[provider]; !ok {
		return domain.PrimerSettings{}, fmt.Errorf("provider %q is not configured", provider)
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	settings, _, err := s.Settings(ctx, provider)
	if err != nil {
		return domain.PrimerSettings{}, err
	}
	change(&settings)
	settings.ProviderAccountID = provider
	if ok, why := s.supported(provider); settings.Enabled && !ok {
		return settings, fmt.Errorf("%w: %s", ErrUnsupported, why)
	}
	normalized, err := Normalize(settings)
	if err != nil {
		return settings, err
	}
	// The save time also keeps slots that already passed from firing.
	normalized.UpdatedAt = s.now()
	if err := s.Store.SavePrimerSettings(ctx, normalized); err != nil {
		return settings, err
	}
	return normalized, nil
}

// History lists recent attempts, newest first.
func (s *Service) History(ctx context.Context, provider string, limit int) ([]domain.PrimerAttempt, error) {
	if _, ok := s.Providers[provider]; !ok {
		return nil, fmt.Errorf("provider %q is not configured", provider)
	}
	return s.Store.ListPrimerAttempts(ctx, provider, limit)
}

// Tick handles every supported provider once.
func (s *Service) Tick(ctx context.Context) {
	providers := make([]string, 0, len(s.Providers))
	for provider := range s.Providers {
		if ok, _ := s.supported(provider); ok {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	for _, provider := range providers {
		if ctx.Err() != nil {
			return
		}
		// A manual ping in progress simply takes this tick's turn.
		if err := s.tickProvider(ctx, provider); err != nil && !errors.Is(err, ErrBusy) {
			log.Printf("window primer %s: %v", provider, err)
		}
	}
}

func (s *Service) tickProvider(ctx context.Context, provider string) error {
	now := s.now()
	s.verifyPending(ctx, provider, now)
	settings, configured, err := s.Settings(ctx, provider)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.lastCheck == nil {
		s.lastCheck = make(map[string]time.Time)
	}
	lastCheck, known := s.lastCheck[provider]
	s.lastCheck[provider] = now
	s.mu.Unlock()
	if !configured || !settings.Enabled {
		return nil
	}
	if !known {
		// First tick of this process: slots missed while the service was
		// stopped count from its last activity, so they still show up as
		// missed. A fresh install, with no activity, invents none.
		lastCheck = s.lastActivity(ctx, provider, settings)
	}
	switch settings.Mode {
	case domain.PrimerSchedule:
		due, missed := DueScheduled(settings, now, lastCheck)
		for _, slot := range missed {
			s.recordSkip(ctx, provider, "schedule", slot, fmt.Sprintf(
				"Missed: Redline was asleep or stopped until more than %s after the scheduled time.",
				EffectiveCatchUp(settings).Round(time.Minute)))
		}
		if due != nil {
			_, err := s.attempt(ctx, provider, settings, *due, "schedule", false)
			return err
		}
	case domain.PrimerReset:
		recent, err := s.Store.ListPrimerAttempts(ctx, provider, 20)
		if err != nil {
			return err
		}
		snapshot, snapshotErr := s.latest(ctx, provider)
		slot := PlanReset(recent, snapshot, snapshotErr == nil, now)
		if slot.FireAt.After(now) {
			return nil
		}
		_, err = s.attempt(ctx, provider, settings, slot, "reset", false)
		return err
	}
	return nil
}

// PlanReset decides reset mode's next slot from the recent attempts and the
// latest usage sample; its FireAt is in the future when nothing is due.
// Tick and Status both use it, so they agree.
//
// The window believed open is the sample's, or the one our own last ping
// opened when the sample shows none: a sample fetched just before a ping
// reads as "no window" and would otherwise prompt another ping straight
// away. Our own window stands until verification, which waits for a sample
// taken well after the ping, rules it out; a ping that could not be checked
// at all is presumed to have worked rather than repeated blind.
func PlanReset(recent []domain.PrimerAttempt, snapshot decision.UsageSnapshot, haveSnapshot bool, now time.Time) Slot {
	if run := ConsecutiveFailures(recent); run.Count > 0 {
		wait := min(idleRetry<<min(run.Count-1, 4), failureBackoffMax)
		if until := run.Last.Add(wait); now.Before(until) {
			return Slot{Kind: SlotBackoff, Key: "backoff", Target: until, FireAt: until}
		}
	}
	var resetsAt *time.Time
	if haveSnapshot && snapshot.Short != nil {
		reset := snapshot.Short.ResetsAt
		resetsAt = &reset
	}
	if fired, ok := lastFired(recent); ok {
		contradicted := fired.Verification == domain.PrimerVerifyUnverified
		opened := fired.StartedAt.Add(WindowLength)
		if fired.WindowResetsAt != nil {
			opened = *fired.WindowResetsAt
		}
		// A window the sample still shows open is real whatever its age: a
		// (forced) ping inside it cannot have opened another.
		sampleOpen := resetsAt != nil && resetsAt.After(now)
		if !contradicted && !sampleOpen && opened.After(now.Add(-resetGrace)) {
			resetsAt = &opened
		}
	}
	claimed := func(key string) bool {
		for _, attempt := range recent {
			if attempt.SlotKey == key {
				return true
			}
		}
		return false
	}
	slot := ResetSlot(resetsAt, now)
	// A reset slot already handled (skipped, say, for an active run) is not
	// due again; reset mode falls back to idle retries until a new window is
	// known. An idle bucket already tried waits for the next one.
	if slot.Kind == SlotReset && claimed(slot.Key) {
		slot = IdleSlot(now)
	}
	if slot.Kind == SlotIdle && claimed(slot.Key) {
		next := NextIdleRetry(now)
		slot.FireAt, slot.Target = next, next
	}
	return slot
}

func lastFired(recent []domain.PrimerAttempt) (domain.PrimerAttempt, bool) {
	for _, attempt := range recent {
		if attempt.Outcome == domain.PrimerFired {
			return attempt, true
		}
	}
	return domain.PrimerAttempt{}, false
}

// lastActivity is the later of the settings' save time and the newest
// attempt's start, or zero when there is neither.
func (s *Service) lastActivity(ctx context.Context, provider string, settings domain.PrimerSettings) time.Time {
	last := settings.UpdatedAt
	if recent, err := s.Store.ListPrimerAttempts(ctx, provider, 1); err == nil && len(recent) > 0 && recent[0].StartedAt.After(last) {
		last = recent[0].StartedAt
	}
	return last
}

// PingNow sends a manual ping. Unless forced it still skips when a window is
// open or a run is active.
func (s *Service) PingNow(ctx context.Context, provider string, force bool) (domain.PrimerAttempt, error) {
	if _, ok := s.Providers[provider]; !ok {
		return domain.PrimerAttempt{}, fmt.Errorf("provider %q is not configured", provider)
	}
	if ok, why := s.supported(provider); !ok {
		return domain.PrimerAttempt{}, fmt.Errorf("%w: %s", ErrUnsupported, why)
	}
	settings, _, err := s.Settings(ctx, provider)
	if err != nil {
		return domain.PrimerAttempt{}, err
	}
	now := s.now()
	slot := Slot{Kind: SlotManual, Key: "manual:" + now.Format(time.RFC3339Nano), Target: now, FireAt: now}
	attempt, err := s.attempt(ctx, provider, settings, slot, "manual", force)
	if err == nil && attempt.ID == 0 {
		err = ErrBusy
	}
	return attempt, err
}

func (s *Service) attempt(
	ctx context.Context, provider string, settings domain.PrimerSettings, slot Slot, trigger string, force bool,
) (domain.PrimerAttempt, error) {
	s.mu.Lock()
	if s.inFlight == nil {
		s.inFlight = make(map[string]bool)
	}
	if s.inFlight[provider] {
		s.mu.Unlock()
		return domain.PrimerAttempt{}, ErrBusy
	}
	s.inFlight[provider] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, provider)
		s.mu.Unlock()
	}()

	attempt := domain.PrimerAttempt{
		ProviderAccountID: provider, Trigger: trigger, SlotKey: slot.Key, TargetAt: slot.Target,
		Outcome: domain.PrimerRunning, StartedAt: s.now(),
	}
	id, err := s.Store.ClaimPrimerSlot(ctx, attempt)
	if errors.Is(err, store.ErrConflict) {
		return domain.PrimerAttempt{}, nil
	}
	if err != nil {
		return domain.PrimerAttempt{}, err
	}
	attempt.ID = id
	if !force {
		if reason, openUntil, skip := s.skipReason(ctx, provider); skip {
			attempt.Outcome, attempt.Reason, attempt.WindowResetsAt = domain.PrimerSkipped, reason, openUntil
			return s.finish(ctx, attempt)
		}
	}
	output, pingErr := s.Pinger.Ping(ctx, settings)
	attempt.Output = output
	if pingErr != nil {
		attempt.Outcome, attempt.Reason = domain.PrimerFailed, pingErr.Error()
		if errors.Is(pingErr, context.Canceled) {
			// A shutdown, not Claude, ended the ping; it may have been sent.
			attempt.Reason = domain.PrimerInterruptedReason
		}
		result, err := s.finish(ctx, attempt)
		if err == nil {
			s.notifyFailure(ctx, provider, attempt.ID)
		}
		return result, err
	}
	attempt.Outcome, attempt.Verification = domain.PrimerFired, domain.PrimerVerifyPending
	attempt.Reason = "Ping sent; checking that a new window opened."
	return s.finish(ctx, attempt)
}

// notifyFailure sends primer.failed once per run of failures rather than on
// every retry: when recording the given attempt is what made the run
// alarming (see FailureRun.Alarming), judged from the stored rows the same
// way as the backoff. The detail stays in the stored attempt rather than
// going to external notification sinks.
func (s *Service) notifyFailure(ctx context.Context, provider string, attemptID int64) {
	if s.Notify == nil {
		return
	}
	recent, err := s.Store.ListPrimerAttempts(ctx, provider, 20)
	if err != nil {
		return
	}
	before := make([]domain.PrimerAttempt, 0, len(recent))
	for _, attempt := range recent {
		if attempt.ID != attemptID {
			before = append(before, attempt)
		}
	}
	if ConsecutiveFailures(before).Alarming() || !ConsecutiveFailures(recent).Alarming() {
		return
	}
	s.Notify(ctx, domain.NotificationEvent{
		Version: 1, Type: domain.EventPrimerFailed, OccurredAt: s.now(), ProviderAccountID: provider,
		Message: "Window primer pings are failing; see `redline primer history --provider " + provider + "`.",
	})
}

func (s *Service) finish(ctx context.Context, attempt domain.PrimerAttempt) (domain.PrimerAttempt, error) {
	attempt.CompletedAt = s.now()
	// Record the outcome even if the request that asked for it was cancelled.
	return attempt, s.Store.FinishPrimerAttempt(context.WithoutCancel(ctx), attempt)
}

// skipReason reports why a ping would be wasted: the provider already has an
// open window, or a Redline run is active and will open the next one itself.
func (s *Service) skipReason(ctx context.Context, provider string) (string, *time.Time, bool) {
	if active, err := s.Store.ActiveRunCount(ctx, provider); err == nil && active > 0 {
		return "A Redline run is active on this provider; its next message opens the window.", nil, true
	}
	snapshot, err := s.fresh(ctx, provider)
	if err == nil && snapshot.Short != nil && snapshot.Short.ResetsAt.After(s.now()) {
		reset := snapshot.Short.ResetsAt
		return fmt.Sprintf("A window is already open; it resets at %s.", reset.Format(time.RFC3339)), &reset, true
	}
	return "", nil, false
}

func (s *Service) recordSkip(ctx context.Context, provider, trigger string, slot Slot, reason string) {
	now := s.now()
	_, err := s.Store.ClaimPrimerSlot(ctx, domain.PrimerAttempt{
		ProviderAccountID: provider, Trigger: trigger, SlotKey: slot.Key, TargetAt: slot.Target,
		Outcome: domain.PrimerSkipped, Reason: reason, StartedAt: now, CompletedAt: now,
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		log.Printf("window primer %s: %v", provider, err)
	}
}

func (s *Service) latest(ctx context.Context, provider string) (decision.UsageSnapshot, error) {
	if s.Latest == nil {
		return decision.UsageSnapshot{}, store.ErrNotFound
	}
	return s.Latest(ctx, provider)
}

// fresh prefers a new sample and falls back to the stored one, for example
// while the usage endpoint is rate limiting.
func (s *Service) fresh(ctx context.Context, provider string) (decision.UsageSnapshot, error) {
	if s.Refresh != nil {
		if snapshot, err := s.Refresh(ctx, provider); err == nil {
			return snapshot, nil
		}
	}
	return s.latest(ctx, provider)
}

func (s *Service) verifyPending(ctx context.Context, provider string, now time.Time) {
	pending, err := s.Store.PendingPrimerVerifications(ctx, provider)
	if err != nil || len(pending) == 0 {
		return
	}
	for _, attempt := range pending {
		// Only a sample taken well after the ping reflects it; one fetched
		// seconds later can still predate the provider's new window.
		settled := attempt.CompletedAt.Add(verifyAfter)
		if now.Before(settled) {
			continue
		}
		snapshot, err := s.latest(ctx, provider)
		if err != nil || snapshot.ObservedAt.Before(settled) || snapshot.Short == nil {
			if s.shouldRefresh(attempt.ID, now) {
				snapshot, err = s.fresh(ctx, provider)
			}
		}
		if err == nil && !snapshot.ObservedAt.Before(settled) {
			// A sample still showing no window may just be early; keep
			// waiting until the give-up deadline before calling it.
			if snapshot.Short != nil || now.Sub(attempt.CompletedAt) >= verifyGiveUp {
				s.recordVerification(ctx, attempt, snapshot, now)
				continue
			}
		}
		if now.Sub(attempt.CompletedAt) >= verifyGiveUp {
			s.verify(ctx, attempt, domain.PrimerVerifyUnknown, nil,
				"Ping sent, but no usage sample arrived to confirm the window.", now)
		}
	}
}

func (s *Service) recordVerification(ctx context.Context, attempt domain.PrimerAttempt, snapshot decision.UsageSnapshot, now time.Time) {
	if snapshot.Short == nil {
		s.verify(ctx, attempt, domain.PrimerVerifyUnverified, nil,
			"Ping sent, but the provider still reports no open window.", now)
		return
	}
	reset := snapshot.Short.ResetsAt
	earliest := attempt.StartedAt.Add(WindowLength - windowSlack)
	latest := attempt.CompletedAt.Add(WindowLength + time.Minute)
	if !reset.Before(earliest) && !reset.After(latest) {
		s.verify(ctx, attempt, domain.PrimerVerified, &reset,
			fmt.Sprintf("Window opened; it resets at %s.", reset.Format(time.RFC3339)), now)
		return
	}
	note := fmt.Sprintf("Ping sent, but the window resets at %s rather than about 5 hours after the ping.", reset.Format(time.RFC3339))
	if reset.After(attempt.StartedAt) && reset.Before(earliest) {
		note = fmt.Sprintf("The ping landed inside a window that was already open (resets %s), so it did not start a new one.", reset.Format(time.RFC3339))
	}
	s.verify(ctx, attempt, domain.PrimerVerifyUnverified, &reset, note, now)
}

// verify records a verdict; a failed write is logged and the attempt stays
// pending so the next tick retries it. A verdict another path recorded
// first (ErrConflict) is left alone, so it cannot be notified twice. A ping
// that was sent but opened no window counts as a failure (the local Claude
// login may not be the monitored account), so the verdict may complete an
// alarming run.
func (s *Service) verify(ctx context.Context, attempt domain.PrimerAttempt, verdict domain.PrimerVerification, resetsAt *time.Time, note string, now time.Time) {
	err := s.Store.VerifyPrimerAttempt(ctx, attempt.ID, verdict, resetsAt, note, now)
	if err != nil && !errors.Is(err, store.ErrConflict) {
		log.Printf("window primer: verify attempt %d: %v", attempt.ID, err)
		return
	}
	s.forgetVerification(attempt.ID)
	if err == nil {
		s.notifyFailure(ctx, attempt.ProviderAccountID, attempt.ID)
	}
}

func (s *Service) shouldRefresh(id int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verifyFetched == nil {
		s.verifyFetched = make(map[int64]time.Time)
	}
	if last, ok := s.verifyFetched[id]; ok && now.Sub(last) < verifyRefreshEvery {
		return false
	}
	s.verifyFetched[id] = now
	return true
}

func (s *Service) forgetVerification(id int64) {
	s.mu.Lock()
	delete(s.verifyFetched, id)
	s.mu.Unlock()
}

// Status summarizes one provider's primer for the API and dashboard.
type Status struct {
	Settings   domain.PrimerSettings `json:"settings"`
	Configured bool                  `json:"configured"`
	Supported  bool                  `json:"supported"`
	// UnsupportedReason explains Supported=false.
	UnsupportedReason string                `json:"unsupported_reason,omitempty"`
	Timezone          string                `json:"timezone"`
	NextPingAt        *time.Time            `json:"next_ping_at,omitempty"`
	WindowOpenUntil   *time.Time            `json:"window_open_until,omitempty"`
	Forecast          []Window              `json:"forecast"`
	LastAttempt       *domain.PrimerAttempt `json:"last_attempt,omitempty"`
}

func (s *Service) Status(ctx context.Context, provider string) (Status, error) {
	if _, ok := s.Providers[provider]; !ok {
		return Status{}, fmt.Errorf("provider %q is not configured", provider)
	}
	settings, configured, err := s.Settings(ctx, provider)
	if err != nil {
		return Status{}, err
	}
	now := s.now()
	supported, why := s.supported(provider)
	status := Status{Settings: settings, Configured: configured, Supported: supported, UnsupportedReason: why,
		Timezone: ZoneName(settings), Forecast: []Window{}}
	snapshot, snapshotErr := s.latest(ctx, provider)
	if snapshotErr == nil && snapshot.Short != nil && snapshot.Short.ResetsAt.After(now) {
		reset := snapshot.Short.ResetsAt
		status.WindowOpenUntil = &reset
	}
	recent, err := s.Store.ListPrimerAttempts(ctx, provider, 20)
	if err != nil {
		return Status{}, err
	}
	if len(recent) > 0 {
		status.LastAttempt = &recent[0]
	}
	if !supported || !settings.Enabled {
		return status, nil
	}
	switch settings.Mode {
	case domain.PrimerSchedule:
		if next := NextScheduled(settings, now, status.WindowOpenUntil); next != nil {
			status.NextPingAt = &next.FireAt
		}
		status.Forecast = ForecastScheduled(settings, now, status.WindowOpenUntil)
	case domain.PrimerReset:
		slot := PlanReset(recent, snapshot, snapshotErr == nil, now)
		status.NextPingAt = &slot.FireAt
		var openUntil *time.Time
		if slot.Kind == SlotReset {
			openUntil = &slot.Target
		}
		status.Forecast = ForecastReset(now, openUntil)
	}
	return status, nil
}
