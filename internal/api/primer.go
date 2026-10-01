package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/primer"
)

func (s *Server) newPrimerService() *primer.Service {
	providers := make(map[string]string, len(s.config.Providers))
	for id, configured := range s.config.Providers {
		providers[id] = strings.ToLower(configured.Provider)
	}
	return &primer.Service{
		Store: s.store, Pinger: primer.ClaudePinger{}, Providers: providers, Now: s.now,
		Latest: s.latestPrimerSnapshot,
		Refresh: func(ctx context.Context, provider string) (decision.UsageSnapshot, error) {
			snapshot, _, err := s.fetchAndStore(ctx, provider)
			return snapshot, err
		},
		Notify: func(ctx context.Context, event domain.NotificationEvent) {
			if s.notifier != nil {
				_ = s.notifier.Notify(ctx, event)
			}
		},
	}
}

func (s *Server) latestPrimerSnapshot(ctx context.Context, provider string) (decision.UsageSnapshot, error) {
	configured, ok := s.config.Providers[provider]
	if !ok {
		return decision.UsageSnapshot{}, errors.New("provider is not configured")
	}
	snapshot, _, err := s.store.LatestSnapshotFromSource(ctx, configured.Provider, s.usageSources.Status(provider).Active)
	return snapshot, err
}

func (s *Server) primerStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.primer.Status(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// primerUpdate is a partial update: omitted fields keep their current value.
type primerUpdate struct {
	Enabled        *bool              `json:"enabled"`
	Mode           *domain.PrimerMode `json:"mode"`
	Times          *[]string          `json:"times"`
	Days           *[]string          `json:"days"`
	Timezone       *string            `json:"timezone"`
	Prompt         *string            `json:"prompt"`
	Model          *string            `json:"model"`
	CatchUpSeconds *int64             `json:"catch_up_seconds"`
}

func (s *Server) updatePrimer(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	var request primerUpdate
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	_, err := s.primer.Update(r.Context(), provider, func(settings *domain.PrimerSettings) {
		if request.Enabled != nil {
			settings.Enabled = *request.Enabled
		}
		if request.Mode != nil {
			settings.Mode = *request.Mode
		}
		if request.Times != nil {
			settings.Times = *request.Times
		}
		if request.Days != nil {
			settings.Days = *request.Days
		}
		if request.Timezone != nil {
			settings.Timezone = *request.Timezone
		}
		if request.Prompt != nil {
			settings.Prompt = *request.Prompt
		}
		if request.Model != nil {
			settings.Model = *request.Model
		}
		if request.CatchUpSeconds != nil {
			settings.CatchUpSeconds = *request.CatchUpSeconds
		}
	})
	if err != nil {
		writePrimerError(w, err)
		return
	}
	s.primerStatus(w, r)
}

// writePrimerError maps the primer's typed errors to statuses; anything
// else (storage, lookup) goes through the shared mapping.
func writePrimerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, primer.ErrInvalid), errors.Is(err, primer.ErrUnsupported):
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
	case errors.Is(err, primer.ErrBusy):
		writeJSON(w, http.StatusConflict, problem{Error: err.Error()})
	default:
		writeError(w, err)
	}
}

func (s *Server) runPrimer(w http.ResponseWriter, r *http.Request) {
	force := false
	if raw := r.URL.Query().Get("force"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, problem{Error: "force must be true or false"})
			return
		}
		force = parsed
	}
	// A client that stops waiting must not kill a ping that may already have
	// reached Claude; the ping carries its own timeout.
	attempt, err := s.primer.PingNow(context.WithoutCancel(r.Context()), r.PathValue("provider"), force)
	if err != nil {
		writePrimerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempt)
}

func (s *Server) primerHistory(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, problem{Error: "limit must be a non-negative integer"})
			return
		}
		limit = parsed
	}
	attempts, err := s.primer.History(r.Context(), r.PathValue("provider"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempts)
}
