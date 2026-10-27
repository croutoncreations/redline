package api

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func waitResetSignalStartup(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) runResetSignals(ctx context.Context) {
	// Avoid an immediate request on every restart, even though feed lockouts
	// are process-local. The injected sleeper keeps tests off real clocks.
	if err := s.resetSignalSleep(ctx, time.Duration(30+rand.IntN(61))*time.Second); err != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	s.resetSignalLoop.Run(ctx)
}

func (s *Server) resetSignals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ResetSignalState())
}

// ResetSignalState is a cached, read-only view. It never causes a fetch. Age
// and snapshot expiry are checked again so a paused monitor cannot keep a
// viable announcement alive indefinitely.
func (s *Server) ResetSignalState() resetsignal.State {
	s.resetSignalMu.Lock()
	state := s.resetSignalState
	s.resetSignalMu.Unlock()
	state.Enabled = s.config.ResetSignals.Enabled
	state.Errors = append([]string(nil), state.Errors...)
	if state.Report != nil {
		sources := append([]resetsignal.SourceResult(nil), state.Report.Sources...)
		for i, result := range sources {
			if result.Signal == nil {
				continue
			}
			signal := *result.Signal
			if signal.ExpiresAt != nil && !signal.ExpiresAt.After(s.now()) {
				signal.Freshness = resetsignal.Stale
			}
			sources[i].Signal = &signal
		}
		report := resetsignal.BuildReport(s.now(), sources)
		state.Report = &report
		state.Announcement = resetsignal.ViableAnnouncement(report)
	}
	return state
}

// ResetSignalCandidate exposes evidence for PR 3, not a scheduler input.
func (s *Server) ResetSignalCandidate() *decision.ResetEvent {
	if !s.config.ResetSignals.Enabled {
		return nil
	}
	a := s.ResetSignalState().Announcement
	if a == nil {
		return nil
	}
	e := a.ResetEventCandidate()
	return &e
}

func (s *Server) pollResetSignals(ctx context.Context, _ string) error {
	if !s.config.ResetSignals.Enabled {
		return nil
	}
	s.resetPollMu.Lock()
	defer s.resetPollMu.Unlock()
	sources := s.resetSignalSources
	if len(sources) == 0 {
		sources = resetsignal.DefaultSources()
	}
	if s.resetNextRead == nil {
		s.resetNextRead = map[string]time.Time{}
	}
	if s.resetDeferred == nil {
		s.resetDeferred = map[string]resetsignal.SourceError{}
	}
	var allowed []resetsignal.Source
	var results []resetsignal.SourceResult
	for _, source := range sources {
		if next := s.resetNextRead[source.Name]; next.After(s.now()) {
			deferred := s.resetDeferred[source.Name]
			deferred.Code = "retry_deferred"
			deferred.Message = "source read deferred until " + next.UTC().Format(time.RFC3339)
			results = append(results, resetsignal.SourceResult{Error: &deferred, Metadata: deferred.Metadata})
			continue
		}
		allowed = append(allowed, source)
	}
	if len(allowed) > 0 {
		fresh := resetsignal.FetchAll(ctx, s.resetSignalFetcher, allowed)
		for _, result := range fresh {
			if result.Error == nil {
				continue
			}
			delay := result.Metadata.RetryAfter
			if result.Metadata.BackoffApplied > delay {
				delay = result.Metadata.BackoffApplied
			}
			if delay > 0 {
				s.resetNextRead[result.Error.Feed] = s.now().Add(delay)
				s.resetDeferred[result.Error.Feed] = *result.Error
			}
		}
		results = append(results, fresh...)
	}
	now := s.now().UTC()
	report := resetsignal.BuildReport(now, results)
	state := resetsignal.State{Enabled: true, Report: &report, Announcement: resetsignal.ViableAnnouncement(report), LastPollAt: &now}
	for _, result := range results {
		if result.Error != nil {
			state.Errors = append(state.Errors, result.Error.Feed+": "+result.Error.Code+": "+result.Error.Message)
		}
	}
	var pollErrors []error
	notify := s.config.ResetSignalsNotify() && s.config.Notifications.Enabled && s.config.NotificationEvents()[domain.EventResetAnnounced] && s.notifier != nil
	if a := state.Announcement; a != nil {
		needed, err := s.store.ObserveResetAnnouncement(ctx, *a, now)
		if err != nil {
			pollErrors = append(pollErrors, err)
		} else if needed && notify {
			claimed, err := s.store.ClaimResetAnnouncement(ctx, *a, now)
			if err != nil {
				pollErrors = append(pollErrors, err)
			} else if claimed {
				if err := s.notifier.Notify(ctx, a.Notification(now, false)); err != nil {
					pollErrors = append(pollErrors, err)
				}
			}
		}
	}
	if notify {
		previous, err := s.store.UnretractedResetAnnouncements(ctx)
		if err != nil {
			pollErrors = append(pollErrors, err)
		}
		for _, a := range previous {
			if !resetsignal.AnnouncementRetracted(report, a) {
				continue
			}
			claimed, err := s.store.ClaimResetAnnouncementRetraction(ctx, a.Key, now)
			if err != nil {
				pollErrors = append(pollErrors, err)
				continue
			}
			if claimed {
				if err := s.notifier.Notify(ctx, a.Notification(now, true)); err != nil {
					pollErrors = append(pollErrors, err)
				}
			}
		}
	}
	for _, err := range pollErrors {
		state.Errors = append(state.Errors, fmt.Sprint(err))
	}
	s.resetSignalMu.Lock()
	s.resetSignalState = state
	s.resetSignalMu.Unlock()
	return errors.Join(pollErrors...)
}
