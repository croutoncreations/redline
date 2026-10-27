package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/resetsignal"
	"github.com/croutoncreations/redline/internal/store"
)

func usableFiresaleAnnouncement(a *resetsignal.Announcement, now time.Time) bool {
	return a != nil && (a.Kind == resetsignal.KindPromised || a.Kind == resetsignal.KindScheduled) && a.WindowEnd != nil && a.WindowEnd.After(now)
}

func (s *Server) hasResetAnnouncementEvent(events []decision.ResetEvent) bool {
	for _, event := range events {
		if event.Source == decision.ResetPredictedGlobal && event.Confidence == "announced" && event.At.After(s.now()) {
			return true
		}
	}
	return false
}

func (s *Server) resetFiresaleState(ctx context.Context) resetsignal.FiresaleState {
	state := resetsignal.FiresaleState{Mode: s.config.ResetSignalsFiresale(), Providers: []string{}}
	if state.Mode == "off" || s.redemptionDisabled {
		return state
	}
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		log.Printf("redline reset firesale state: %v", err)
		return state
	}
	for _, sale := range sales {
		if !sale.Deadline.After(s.now()) || !strings.EqualFold(strings.TrimSpace(s.config.Providers[sale.ProviderAccountID].Provider), "codex") {
			continue
		}
		if !state.Active {
			state.Active = true
			state.Mode = sale.Mode
			state.AnnouncementKey = sale.AnnouncementKey
			deadline, started := sale.Deadline, sale.StartedAt
			state.Deadline, state.StartedAt = &deadline, &started
		}
		if sale.AnnouncementKey == state.AnnouncementKey {
			state.Providers = append(state.Providers, sale.ProviderAccountID)
		}
	}
	return state
}

func (s *Server) resetAnnouncementNotification(a resetsignal.Announcement, now time.Time) domain.NotificationEvent {
	event := a.Notification(now, false)
	mode, action := s.config.ResetSignalsFiresale(), ""
	if usableFiresaleAnnouncement(&a, now) && mode == "notify" && !s.redemptionDisabled {
		action = "start"
		event.Message += " Start a firesale to run queued work before it lands."
	}
	if mode == "auto" && s.resetFiresaleState(context.Background()).AnnouncementKey == a.Key {
		action = "active"
	}
	event.Data["firesale_mode"], event.Data["firesale_action"] = mode, action
	return event
}

func (s *Server) resetRetractionNotification(a resetsignal.Announcement, now time.Time) domain.NotificationEvent {
	event := a.Notification(now, true)
	event.Data["firesale_mode"], event.Data["firesale_action"] = s.config.ResetSignalsFiresale(), ""
	return event
}

func (s *Server) notifyResetFiresale(ctx context.Context, sale store.ResetSignalFiresale, reason, completionSource string) error {
	if s.notifier == nil {
		return nil
	}
	typ := domain.EventFiresaleStarted
	if reason != "" {
		typ = domain.EventFiresaleEnded
	}
	event := domain.NotificationEvent{
		Version: 1, Type: typ, OccurredAt: s.now().UTC(), ProviderAccountID: sale.ProviderAccountID,
		Data: map[string]string{
			"trigger": "reset_announcement", "announcement_key": sale.AnnouncementKey,
			"deadline": sale.Deadline.UTC().Format(time.RFC3339Nano), "mode": sale.Mode,
			"reason": reason, "account": sale.Announcement.Account,
		},
	}
	if reason == "completed" && completionSource != "" {
		event.Data["completion_source"] = completionSource
	}
	return s.notifier.Notify(ctx, notification.WithMessage(event, "codex"))
}

// Caller holds resetFiresaleMu. A new announcement cannot replace an existing
// opt-in or change its identity; auto mode waits for that sale to end.
func (s *Server) startResetFiresale(ctx context.Context, a resetsignal.Announcement, manual bool) error {
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return err
	}
	for _, sale := range sales {
		if sale.AnnouncementKey != a.Key {
			if manual {
				return fmt.Errorf("%w: announcement %s already has an active firesale until %s; run redline reset firesale stop first", store.ErrConflict, sale.AnnouncementKey, sale.Deadline.UTC().Format(time.RFC3339))
			}
			return nil
		}
	}
	mode := s.config.ResetSignalsFiresale()
	for id, provider := range s.config.Providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Provider), "codex") {
			continue
		}
		claimed, err := s.store.StartResetSignalFiresale(ctx, id, mode, a, s.now(), manual)
		if err != nil {
			return err
		}
		if claimed {
			sale := store.ResetSignalFiresale{AnnouncementKey: a.Key, ProviderAccountID: id, Mode: mode, Deadline: *a.WindowEnd, Announcement: a}
			if err := s.notifyResetFiresale(ctx, sale, "", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// A running sale survives missing/stale feeds; only an explicit lifecycle
// event ends it. Claims precede notification delivery (at most once).
func (s *Server) reconcileResetSignalFiresales(ctx context.Context) error {
	s.resetFiresaleMu.Lock()
	defer s.resetFiresaleMu.Unlock()
	state := s.ResetSignalState()
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return err
	}
	for _, sale := range sales {
		if a := state.Announcement; usableFiresaleAnnouncement(a, s.now()) && a.Key == sale.AnnouncementKey && !a.WindowEnd.Equal(sale.Deadline) {
			changed, err := s.store.UpdateResetSignalFiresaleDeadline(ctx, sale.ID, *a)
			if err != nil {
				return err
			}
			if changed {
				log.Printf("redline reset announcement %s firesale deadline changed: %s -> %s", a.Key, sale.Deadline.UTC().Format(time.RFC3339), a.WindowEnd.UTC().Format(time.RFC3339))
			}
			sale.Deadline, sale.Announcement = *a.WindowEnd, *a
		}
		reason := ""
		completionSource := firesaleCompletionSource(state, sale)
		switch {
		case s.config.ResetSignalsFiresale() == "off" || s.redemptionDisabled || !strings.EqualFold(strings.TrimSpace(s.config.Providers[sale.ProviderAccountID].Provider), "codex"):
			reason = "disabled"
		case !sale.Deadline.After(s.now()):
			reason = "deadline_passed"
		case state.Report != nil && resetsignal.AnnouncementRetracted(*state.Report, sale.Announcement):
			reason = "retracted"
		case completionSource != "":
			reason = "completed"
		}
		if reason != "" {
			claimed, err := s.store.EndResetSignalFiresale(ctx, sale.ID, reason, s.now())
			if err != nil {
				return err
			}
			if claimed {
				if err := s.notifyResetFiresale(ctx, sale, reason, completionSource); err != nil {
					return err
				}
			}
		}
	}
	if s.config.ResetSignalsFiresale() == "auto" && !s.redemptionDisabled && usableFiresaleAnnouncement(state.Announcement, s.now()) {
		return s.startResetFiresale(ctx, *state.Announcement, false)
	}
	return nil
}

func firesaleCompletionSource(state resetsignal.State, sale store.ResetSignalFiresale) string {
	announcedAt := sale.StartedAt
	if sale.Announcement.PublishedAt != nil {
		announcedAt = *sale.Announcement.PublishedAt
	}
	if a := state.Announcement; a != nil && a.Kind == resetsignal.KindCompleted {
		if a.Key == sale.AnnouncementKey || (a.PublishedAt != nil && a.PublishedAt.After(announcedAt)) {
			return "announcement"
		}
	}
	if state.Report != nil {
		if completion := resetsignal.SelectedCompletion(*state.Report); completion != nil && completion.PublishedAt.After(announcedAt) {
			if completion.AnnouncementID != "" || completion.ClaimedOriginalURL != "" {
				return "announcement"
			}
			return "tracker_reset"
		}
	}
	return ""
}

func (s *Server) stopResetFiresale(ctx context.Context, key string) error {
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return err
	}
	for _, sale := range sales {
		if key != "" && sale.AnnouncementKey != key {
			return fmt.Errorf("%w: active firesale belongs to announcement %s", store.ErrConflict, sale.AnnouncementKey)
		}
	}
	for _, sale := range sales {
		claimed, err := s.store.EndResetSignalFiresale(ctx, sale.ID, "stopped", s.now())
		if err != nil {
			return err
		}
		if claimed {
			if err := s.notifyResetFiresale(ctx, sale, "stopped", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) resetSignalFiresale(w http.ResponseWriter, r *http.Request) {
	if authorized, cookie := s.authorized(r); !authorized || cookie {
		writeJSON(w, http.StatusForbidden, problem{Error: "reset firesales require the local API token"})
		return
	}
	var body *struct {
		AnnouncementKey string `json:"announcement_key"`
		Action          string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil || body == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "request body must be an object with announcement_key and action"})
		return
	}
	if body.Action != "start" && body.Action != "stop" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "action must be start or stop"})
		return
	}
	if s.redemptionDisabled {
		writeError(w, fmt.Errorf("%w: reset firesales are disabled in demo mode", store.ErrConflict))
		return
	}
	// Serialize mutations with polls so a start cannot opt into a report that
	// was just replaced by a completion or retraction.
	s.resetPollMu.Lock()
	defer s.resetPollMu.Unlock()
	if err := s.reconcileResetSignalFiresales(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	s.resetFiresaleMu.Lock()
	var err error
	if body.Action == "stop" {
		err = s.stopResetFiresale(r.Context(), body.AnnouncementKey)
	} else {
		state := s.ResetSignalState()
		if s.config.ResetSignalsFiresale() == "off" || !usableFiresaleAnnouncement(state.Announcement, s.now()) || state.Announcement.Key != body.AnnouncementKey {
			err = fmt.Errorf("%w: no matching viable announcement with a future deadline, or firesale is off", store.ErrConflict)
		} else {
			err = s.startResetFiresale(r.Context(), *state.Announcement, true)
		}
	}
	s.resetFiresaleMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ResetSignalState().Firesale)
}
