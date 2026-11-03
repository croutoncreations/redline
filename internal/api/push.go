package api

import (
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/croutoncreations/redline/internal/push"
	"github.com/croutoncreations/redline/internal/store"
)

var pushProvider = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func devicePrincipal(w http.ResponseWriter, r *http.Request) string {
	id := requestPrincipal(r).deviceID
	if id == "" {
		writeJSON(w, http.StatusForbidden, problem{Error: "this action requires a device session"})
	}
	return id
}
func (s *Server) putPushRegistration(w http.ResponseWriter, r *http.Request) {
	id := devicePrincipal(w, r)
	if id == "" {
		return
	}
	var body *struct {
		Platform string `json:"platform"`
		Provider string `json:"provider"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "invalid push registration"})
		return
	}
	if body == nil || (body.Platform != "android" && body.Platform != "ios") || !pushProvider.MatchString(body.Provider) || utf8.RuneCountInString(body.Token) < 1 || utf8.RuneCountInString(body.Token) > 4096 {
		writeJSON(w, http.StatusBadRequest, problem{Error: "platform must be android or ios, provider must match [a-z0-9_-]{1,32}, and token must contain 1..4096 characters"})
		return
	}
	if err := s.store.UpsertPushRegistration(r.Context(), store.PushRegistration{DeviceSessionID: id, Platform: body.Platform, Provider: body.Provider, Token: body.Token}, s.now().UTC()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) deletePushRegistration(w http.ResponseWriter, r *http.Request) {
	id := devicePrincipal(w, r)
	if id == "" {
		return
	}
	if err := s.store.DeletePushRegistration(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) listPushRegistrations(w http.ResponseWriter, r *http.Request) {
	if !requireOwner(w, r) {
		return
	}
	registrations, err := s.store.ActivePushRegistrations(r.Context(), s.sessionIssuer())
	if err != nil {
		writeError(w, err)
		return
	}
	stats := push.Stats{}
	if s.push != nil {
		stats = s.push.Stats()
	}
	writeJSON(w, http.StatusOK, struct {
		Registrations []store.PushRegistration `json:"registrations"`
		push.Stats
	}{registrations, stats})
}
