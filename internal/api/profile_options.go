package api

import (
	"net/http"
	"strings"
	"time"
)

const harnessCatalogTTL = 5 * time.Minute

func (s *Server) profileOptions(w http.ResponseWriter, r *http.Request) {
	refresh := strings.EqualFold(r.URL.Query().Get("refresh"), "true")
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if refresh || s.catalogAt.IsZero() || s.now().Sub(s.catalogAt) >= harnessCatalogTTL {
		s.catalog = s.discovery.Discover(r.Context())
		s.catalogAt = s.now()
	}
	writeJSON(w, http.StatusOK, s.catalog)
}
