package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
	"github.com/croutoncreations/redline/internal/store"
)

func (s *Server) firesaleAccountLabel(id string) string { return id }

func (s *Server) firesaleAccountUsable(id string) error {
	if _, ok := s.config.Providers[id]; !ok {
		return store.ErrNotFound
	}
	return nil
}

type manualFiresaleNotice struct {
	sale   store.ManualFiresale
	reason string
}

// Claims persist under manualFiresaleMu; hooks run only after it is released.
func (s *Server) deliverManualFiresaleNotices(ctx context.Context, notices []manualFiresaleNotice) error {
	if s.notifier == nil {
		return nil
	}
	var errs []error
	for _, notice := range notices {
		sale := notice.sale
		typ := domain.EventFiresaleStarted
		if notice.reason != "" {
			typ = domain.EventFiresaleEnded
		}
		event := domain.NotificationEvent{Version: 1, Type: typ, OccurredAt: s.now().UTC(), ProviderAccountID: sale.ProviderAccountID,
			Data: map[string]string{"trigger": "manual", "account": sale.ProviderAccountID, "deadline": sale.Deadline.UTC().Format(time.RFC3339Nano), "started_by": sale.StartedBy, "reason": notice.reason}}
		errs = append(errs, s.notifier.Notify(ctx, notification.WithMessage(event, s.firesaleAccountLabel(sale.ProviderAccountID))))
	}
	return errors.Join(errs...)
}

// reconcileManualFiresales ends expired or completed sales and returns only
// storage errors. Notices for committed endings are always delivered; a hook
// failure is logged, since the state change has already happened.
func (s *Server) reconcileManualFiresales(ctx context.Context) error {
	notices, err := s.reconcileManualFiresalesLocked(ctx)
	s.notifyManualFiresales(ctx, notices)
	return err
}

// notifyManualFiresales delivers lifecycle notices after the change is
// persisted, logging rather than returning failures: a client must never see
// an error for a start or stop that took effect, then retry against changed state.
func (s *Server) notifyManualFiresales(ctx context.Context, notices []manualFiresaleNotice) {
	if err := s.deliverManualFiresaleNotices(ctx, notices); err != nil {
		log.Printf("redline manual firesale notification: %v", err)
	}
}

func (s *Server) reconcileManualFiresalesLocked(ctx context.Context) ([]manualFiresaleNotice, error) {
	s.manualFiresaleMu.Lock()
	defer s.manualFiresaleMu.Unlock()
	sales, err := s.store.ActiveManualFiresales(ctx)
	if err != nil {
		return nil, err
	}
	var notices []manualFiresaleNotice
	var errs []error
	for _, sale := range sales {
		reason := ""
		if !sale.Deadline.After(s.now()) {
			reason = "deadline_passed"
		} else if sale.BaselineWeeklyResetsAt != nil {
			provider, ok := s.config.Providers[sale.ProviderAccountID]
			if ok {
				snapshot, _, err := s.store.LatestSnapshot(ctx, provider.Provider)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					errs = append(errs, err)
					continue
				}
				if err == nil && !snapshot.ObservedAt.Before(sale.StartedAt) && !snapshot.ObservedAt.After(s.now()) && snapshot.Weekly.ResetsAt.Sub(*sale.BaselineWeeklyResetsAt) >= newWindowShift {
					reason = "completed"
				}
			}
		}
		if reason == "" {
			continue
		}
		claimed, err := s.store.EndManualFiresale(ctx, sale.ID, reason, s.now())
		if err != nil {
			errs = append(errs, err)
		} else if claimed {
			notices = append(notices, manualFiresaleNotice{sale: sale, reason: reason})
		}
	}
	return notices, errors.Join(errs...)
}

func (s *Server) manualFiresales(w http.ResponseWriter, r *http.Request) {
	if err := s.reconcileManualFiresales(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	sales, err := s.store.ActiveManualFiresales(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	type eligibleAccount struct {
		Account           string     `json:"account"`
		Label             string     `json:"label"`
		Provider          string     `json:"provider"`
		WeeklyResetsAt    *time.Time `json:"weekly_resets_at"`
		ReasonUnavailable *string    `json:"reason_unavailable"`
	}
	eligible := []eligibleAccount{}
	ids := make([]string, 0, len(s.config.Providers))
	for id := range s.config.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.firesaleAccountUsable(id) != nil {
			continue
		}
		provider := s.config.Providers[id]
		item := eligibleAccount{Account: id, Label: s.firesaleAccountLabel(id), Provider: provider.Provider}
		snapshot, _, err := s.store.LatestSnapshot(r.Context(), provider.Provider)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, err)
			return
		}
		reason := ""
		switch {
		case errors.Is(err, store.ErrNotFound) || snapshot.Weekly.ResetsAt.IsZero():
			reason = "weekly usage not reported"
		case snapshot.Weekly.Remaining <= 0:
			reason = "weekly allowance used up"
		}
		if err == nil && !snapshot.Weekly.ResetsAt.IsZero() {
			reset := snapshot.Weekly.ResetsAt
			item.WeeklyResetsAt = &reset
		}
		paused, err := s.store.ProviderPaused(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if paused {
			reason = "provider paused"
		}
		if reason != "" {
			item.ReasonUnavailable = &reason
		}
		eligible = append(eligible, item)
	}
	writeJSON(w, http.StatusOK, struct {
		Manual   []store.ManualFiresale `json:"manual"`
		Eligible []eligibleAccount      `json:"eligible"`
	}{sales, eligible})
}

func (s *Server) startManualFiresale(w http.ResponseWriter, r *http.Request) {
	var body *struct {
		Account string `json:"account"`
		Until   string `json:"until"`
	}
	if err := decodeJSON(r, &body); err != nil || body == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "request body must be an object with account and until"})
		return
	}
	if body.Account == "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "account is required"})
		return
	}
	if err := s.firesaleAccountUsable(body.Account); err != nil {
		writeError(w, err)
		return
	}
	if s.redemptionDisabled {
		writeError(w, fmt.Errorf("%w: manual firesales are disabled in demo mode", store.ErrConflict))
		return
	}
	now := s.now()
	var deadline time.Time
	if body.Until != "weekly_reset" {
		duration, err := time.ParseDuration(body.Until)
		if err != nil || duration < 5*time.Minute || duration > 7*24*time.Hour {
			writeJSON(w, http.StatusBadRequest, problem{Error: "until must be weekly_reset or a duration from 5m to 168h"})
			return
		}
		deadline = now.Add(duration)
	}
	by := "owner"
	if p := requestPrincipal(r); p.deviceID != "" {
		sessions, err := s.store.DeviceSessions(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		for _, session := range sessions {
			if session.ID == p.deviceID {
				by = "device:" + session.Name
				break
			}
		}
	}
	if err := s.reconcileManualFiresales(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	s.manualFiresaleMu.Lock()
	snapshot, _, err := s.store.LatestSnapshot(r.Context(), s.config.Providers[body.Account].Provider)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.manualFiresaleMu.Unlock()
		writeError(w, err)
		return
	}
	var baseline *time.Time
	if err == nil && !snapshot.Weekly.ResetsAt.IsZero() {
		reset := snapshot.Weekly.ResetsAt
		baseline = &reset
	}
	if body.Until == "weekly_reset" {
		if baseline == nil || !baseline.After(now) {
			s.manualFiresaleMu.Unlock()
			writeError(w, fmt.Errorf("%w: weekly reset is unknown", store.ErrConflict))
			return
		}
		deadline = *baseline
	}
	sale, claimed, err := s.store.StartManualFiresale(r.Context(), body.Account, now, deadline, by, baseline)
	s.manualFiresaleMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if claimed {
		status = http.StatusCreated
		s.notifyManualFiresales(r.Context(), []manualFiresaleNotice{{sale: sale}})
	}
	writeJSON(w, status, sale)
}

func (s *Server) stopManualFiresale(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	if err := s.firesaleAccountUsable(account); err != nil {
		writeError(w, err)
		return
	}
	if err := s.reconcileManualFiresales(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	s.manualFiresaleMu.Lock()
	sale, err := s.store.ActiveManualFiresaleFor(r.Context(), account)
	claimed := false
	if err == nil {
		claimed, err = s.store.EndManualFiresale(r.Context(), sale.ID, "stopped", s.now())
	}
	s.manualFiresaleMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	if !claimed {
		writeError(w, store.ErrNotFound)
		return
	}
	s.notifyManualFiresales(r.Context(), []manualFiresaleNotice{{sale: sale, reason: "stopped"}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) manualFiresaleActive(ctx context.Context, account string) (bool, error) {
	if err := s.reconcileManualFiresales(ctx); err != nil {
		return false, err
	}
	sale, err := s.store.ActiveManualFiresaleFor(ctx, account)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil && sale.Deadline.After(s.now()) && !s.redemptionDisabled, err
}
