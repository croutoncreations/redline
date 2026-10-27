package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	if !requireOwner(w, r) {
		return
	}
	var body *struct {
		Event    string `json:"event"`
		Provider string `json:"provider"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if body == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "request body must be an object"})
		return
	}
	if body.Event == "" {
		body.Event = domain.EventResetExpiring
	}
	if body.Provider == "" {
		ids := make([]string, 0, len(s.config.Providers))
		for id := range s.config.Providers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			body.Provider = ids[0]
		}
		for _, id := range ids {
			if strings.EqualFold(s.config.Providers[id].Provider, "codex") {
				body.Provider = id
				break
			}
		}
	}
	provider, ok := s.config.Providers[body.Provider]
	if !ok {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider is not configured"})
		return
	}
	event, err := notification.Sample(body.Event, body.Provider, provider.Provider, s.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if !s.config.Notifications.Enabled {
		writeJSON(w, http.StatusConflict, problem{Error: "notifications are disabled; set notifications.enabled: true in your config (command may be empty for native Mac alerts)"})
		return
	}
	delivery, err := configuredNotifier(s.config, s.store, s.now).Test(r.Context(), event)
	if err != nil && delivery.ID == 0 {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, delivery)
}
