package api

import (
	"context"
	"errors"
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

// firesaleNotice is a claimed lifecycle change awaiting delivery. Changes are
// persisted under resetFiresaleMu; delivery happens after it is released, so
// a slow or failing hook can neither hold the lock nor stop later accounts.
type firesaleNotice struct {
	sale             store.ResetSignalFiresale
	reason           string
	completionSource string
}

// deliverFiresaleNotices attempts every notice and joins the failures.
func (s *Server) deliverFiresaleNotices(ctx context.Context, notices []firesaleNotice) error {
	var errs []error
	for _, n := range notices {
		if err := s.notifyResetFiresale(ctx, n.sale, n.reason, n.completionSource); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Caller holds resetFiresaleMu. A new announcement cannot replace an existing
// opt-in or change its identity; auto mode waits for that sale to end.
func (s *Server) startResetFiresale(ctx context.Context, a resetsignal.Announcement, manual bool) ([]firesaleNotice, error) {
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return nil, err
	}
	for _, sale := range sales {
		if sale.AnnouncementKey != a.Key {
			if manual {
				return nil, fmt.Errorf("%w: announcement %s already has an active firesale until %s; run redline reset firesale stop first", store.ErrConflict, sale.AnnouncementKey, sale.Deadline.UTC().Format(time.RFC3339))
			}
			return nil, nil
		}
	}
	mode := s.config.ResetSignalsFiresale()
	var notices []firesaleNotice
	var errs []error
	for id, provider := range s.config.Providers {
		if !strings.EqualFold(strings.TrimSpace(provider.Provider), "codex") {
			continue
		}
		claimed, err := s.store.StartResetSignalFiresale(ctx, id, mode, a, s.now(), manual)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if claimed {
			notices = append(notices, firesaleNotice{sale: store.ResetSignalFiresale{AnnouncementKey: a.Key, ProviderAccountID: id, Mode: mode, Deadline: *a.WindowEnd, Announcement: a}})
		}
	}
	return notices, errors.Join(errs...)
}

// A running sale survives missing/stale feeds; only an explicit lifecycle
// event ends it. Claims precede notification delivery (at most once).
func (s *Server) reconcileResetSignalFiresales(ctx context.Context) error {
	notices, err := s.reconcileResetSignalFiresalesLocked(ctx)
	return errors.Join(err, s.deliverFiresaleNotices(ctx, notices))
}

func (s *Server) reconcileResetSignalFiresalesLocked(ctx context.Context) ([]firesaleNotice, error) {
	s.resetFiresaleMu.Lock()
	defer s.resetFiresaleMu.Unlock()
	state := s.ResetSignalState()
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return nil, err
	}
	var notices []firesaleNotice
	var errs []error
	for _, sale := range sales {
		if a := state.Announcement; usableFiresaleAnnouncement(a, s.now()) && a.Key == sale.AnnouncementKey && !a.WindowEnd.Equal(sale.Deadline) {
			changed, err := s.store.UpdateResetSignalFiresaleDeadline(ctx, sale.ID, *a)
			if err != nil {
				errs = append(errs, err)
				continue
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
				errs = append(errs, err)
				continue
			}
			if claimed {
				notices = append(notices, firesaleNotice{sale: sale, reason: reason, completionSource: completionSource})
			}
		}
	}
	if s.config.ResetSignalsFiresale() == "auto" && !s.redemptionDisabled && usableFiresaleAnnouncement(state.Announcement, s.now()) {
		started, err := s.startResetFiresale(ctx, *state.Announcement, false)
		notices = append(notices, started...)
		errs = append(errs, err)
	}
	return notices, errors.Join(errs...)
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

// stopResetFiresale ends every matching sale before reporting any error, so a
// failure on one account never leaves another account's sale running.
func (s *Server) stopResetFiresale(ctx context.Context, key string) ([]firesaleNotice, error) {
	sales, err := s.store.ActiveResetSignalFiresales(ctx)
	if err != nil {
		return nil, err
	}
	for _, sale := range sales {
		if key != "" && sale.AnnouncementKey != key {
			return nil, fmt.Errorf("%w: active firesale belongs to announcement %s", store.ErrConflict, sale.AnnouncementKey)
		}
	}
	var notices []firesaleNotice
	var errs []error
	for _, sale := range sales {
		claimed, err := s.store.EndResetSignalFiresale(ctx, sale.ID, "stopped", s.now())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if claimed {
			notices = append(notices, firesaleNotice{sale: sale, reason: "stopped"})
		}
	}
	return notices, errors.Join(errs...)
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
	var notices []firesaleNotice
	if body.Action == "stop" {
		notices, err = s.stopResetFiresale(r.Context(), body.AnnouncementKey)
	} else {
		state := s.ResetSignalState()
		if s.config.ResetSignalsFiresale() == "off" || !usableFiresaleAnnouncement(state.Announcement, s.now()) || state.Announcement.Key != body.AnnouncementKey {
			err = fmt.Errorf("%w: no matching viable announcement with a future deadline, or firesale is off", store.ErrConflict)
		} else {
			notices, err = s.startResetFiresale(r.Context(), *state.Announcement, true)
		}
	}
	s.resetFiresaleMu.Unlock()
	// The lifecycle change is already persisted; a delivery failure is still
	// reported to the caller, who asked for this change directly.
	err = errors.Join(err, s.deliverFiresaleNotices(r.Context(), notices))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.ResetSignalState().Firesale)
}
