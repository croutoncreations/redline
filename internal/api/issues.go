package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/issues"
	"github.com/croutoncreations/redline/internal/store"
)

type issueDispatchKey struct{}

func (s *Server) listIssueSources(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.IssueSources(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) createIssueSource(w http.ResponseWriter, r *http.Request) {
	var source domain.IssueSource
	if err := decodeJSON(r, &source); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	issues.Defaults(&source)
	source.LastSyncedAt = nil
	source.NextSyncAt = nil
	source.LastError = ""
	if err := issues.Validate(source); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	profile, err := s.store.GetProfile(r.Context(), source.ExecutionProfileID)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err = s.issues.CheckRepository(ctx, source, profile); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if profile.BaseBranch == "" || profile.BaseBranch == "HEAD" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "Issue profiles require a named base branch"})
		return
	}
	if err = s.store.CreateIssueSource(ctx, source); err != nil {
		writeJSON(w, http.StatusConflict, problem{Error: "Issue source ID already exists or cannot be saved"})
		return
	}
	writeJSON(w, http.StatusCreated, source)
}
func (s *Server) issuePreview(w http.ResponseWriter, r *http.Request) {
	source, err := s.store.IssueSource(r.Context(), r.PathValue("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rows, err := s.issues.Preview(ctx, source)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, problem{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
func (s *Server) syncIssueSource(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	rows, err := s.issues.Sync(ctx, r.PathValue("source"), true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, problem{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
func (s *Server) controlIssueSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.store.IssueSource(r.Context(), r.PathValue("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	switch r.PathValue("control") {
	case "enable":
		source.Enabled = true
	case "disable":
		source.Enabled = false
	default:
		writeJSON(w, http.StatusBadRequest, problem{Error: "Use enable or disable"})
		return
	}
	if err = s.store.SetIssueSourceEnabled(r.Context(), source.ID, source.Enabled); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}
func (s *Server) listIssueItems(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.IssueItems(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
func (s *Server) retryIssue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	id, err := s.issues.Retry(ctx, r.PathValue("item"))
	if err != nil {
		writeIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"item_id": id})
}
func (s *Server) retryIssueDelivery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.issues.RetryDelivery(ctx, r.PathValue("task")); err != nil {
		writeIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
}
func writeIssueError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, problem{Error: fmt.Sprint(err)})
}

func (s *Server) updateIssueSource(w http.ResponseWriter, r *http.Request) {
	old, err := s.store.IssueSource(r.Context(), r.PathValue("source"))
	if err != nil {
		writeError(w, err)
		return
	}
	var source domain.IssueSource
	if err = decodeJSON(r, &source); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	issues.Defaults(&source)
	if source.ID != old.ID || source.Host != old.Host || source.Repository != old.Repository || source.ExecutionProfileID != old.ExecutionProfileID {
		writeJSON(w, http.StatusBadRequest, problem{Error: "Source identity, repository, and execution profile cannot change; create a new source"})
		return
	}
	if err = issues.Validate(source); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	source.LastSyncedAt = old.LastSyncedAt
	source.NextSyncAt = nil
	source.LastError = old.LastError
	if err = s.store.UpdateIssueSourceConfig(r.Context(), source); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}
