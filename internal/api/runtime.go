package api

import (
	"net/http"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/hermes"
)

func (s *Server) discoverRuntimeImports(w http.ResponseWriter, _ *http.Request) {
	result := make([]domain.RuntimeConnection, 0, 1)
	if connection, err := hermes.DiscoverDesktopConnection(); err == nil {
		result = append(result, connection)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listRuntimeConnections(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListRuntimeConnections(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []domain.RuntimeConnection{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createRuntimeConnection(w http.ResponseWriter, r *http.Request) {
	var item domain.RuntimeConnection
	if err := decodeJSON(r, &item); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	item.CreatedAt = s.now().UTC()
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	if err := s.store.CreateRuntimeConnection(r.Context(), item, item.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getRuntimeConnection(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetRuntimeConnection(r.Context(), r.PathValue("connection"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type runtimeConnectionUpdate struct {
	Runtime           *string `json:"runtime"`
	Transport         *string `json:"transport"`
	URL               *string `json:"url"`
	CredentialSource  *string `json:"credential_source"`
	CredentialRef     *string `json:"credential_ref"`
	DesktopConfigPath *string `json:"desktop_config_path"`
	MaxConcurrentRuns *int    `json:"max_concurrent_runs"`
}

func (s *Server) updateRuntimeConnection(w http.ResponseWriter, r *http.Request) {
	var request runtimeConnectionUpdate
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	item, err := s.store.GetRuntimeConnection(r.Context(), r.PathValue("connection"))
	if err != nil {
		writeError(w, err)
		return
	}
	if request.Runtime != nil {
		item.Runtime = *request.Runtime
	}
	if request.Transport != nil {
		item.Transport = *request.Transport
	}
	if request.URL != nil {
		item.URL = *request.URL
	}
	if request.CredentialSource != nil {
		item.CredentialSource = *request.CredentialSource
	}
	if request.CredentialRef != nil {
		item.CredentialRef = *request.CredentialRef
	}
	if request.DesktopConfigPath != nil {
		item.DesktopConfigPath = *request.DesktopConfigPath
	}
	if request.MaxConcurrentRuns != nil {
		item.MaxConcurrentRuns = *request.MaxConcurrentRuns
	}
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	if err := s.store.UpdateRuntimeConnection(r.Context(), item); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteRuntimeConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRuntimeConnection(r.Context(), r.PathValue("connection")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discoverRuntimeConnection(w http.ResponseWriter, r *http.Request) {
	var options hermes.DiscoveryOptions
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &options); err != nil {
			writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
			return
		}
	}
	item, err := s.store.GetRuntimeConnection(r.Context(), r.PathValue("connection"))
	if err != nil {
		writeError(w, err)
		return
	}
	if item.Runtime != "hermes" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "runtime discovery is not supported"})
		return
	}
	result, err := s.hermes.Discover(r.Context(), item)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result.View(options))
}

func (s *Server) listRuntimeJobs(w http.ResponseWriter, r *http.Request) {
	item, ok := s.hermesRuntimeConnection(w, r)
	if !ok {
		return
	}
	jobs, err := s.hermes.ListJobs(r.Context(), item)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) triggerRuntimeJob(w http.ResponseWriter, r *http.Request) {
	item, ok := s.hermesRuntimeConnection(w, r)
	if !ok {
		return
	}
	job, err := s.hermes.TriggerJob(r.Context(), item, r.PathValue("job"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) hermesRuntimeConnection(w http.ResponseWriter, r *http.Request) (domain.RuntimeConnection, bool) {
	item, err := s.store.GetRuntimeConnection(r.Context(), r.PathValue("connection"))
	if err != nil {
		writeError(w, err)
		return domain.RuntimeConnection{}, false
	}
	if item.Runtime != "hermes" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "runtime jobs are only supported for Hermes"})
		return domain.RuntimeConnection{}, false
	}
	return item, true
}

func (s *Server) listAgentContexts(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAgentContexts(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []domain.AgentContext{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createAgentContext(w http.ResponseWriter, r *http.Request) {
	var item domain.AgentContext
	if err := decodeJSON(r, &item); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if _, err := s.store.GetRuntimeConnection(r.Context(), item.RuntimeConnectionID); err != nil {
		writeError(w, err)
		return
	}
	item.CreatedAt = s.now().UTC()
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	if err := s.store.CreateAgentContext(r.Context(), item, item.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getAgentContext(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetAgentContext(r.Context(), r.PathValue("context"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type agentContextUpdate struct {
	RuntimeConnectionID *string `json:"runtime_connection_id"`
	Profile             *string `json:"profile"`
	Agent               *string `json:"agent"`
	Project             *string `json:"project"`
	WorkingDirectory    *string `json:"working_directory"`
	SessionMode         *string `json:"session_mode"`
	MaxConcurrentRuns   *int    `json:"max_concurrent_runs"`
}

func (s *Server) updateAgentContext(w http.ResponseWriter, r *http.Request) {
	var request agentContextUpdate
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	item, err := s.store.GetAgentContext(r.Context(), r.PathValue("context"))
	if err != nil {
		writeError(w, err)
		return
	}
	if request.RuntimeConnectionID != nil {
		item.RuntimeConnectionID = *request.RuntimeConnectionID
	}
	if request.Profile != nil {
		item.Profile = *request.Profile
	}
	if request.Agent != nil {
		item.Agent = *request.Agent
	}
	if request.Project != nil {
		item.Project = *request.Project
	}
	if request.WorkingDirectory != nil {
		item.WorkingDirectory = *request.WorkingDirectory
	}
	if request.SessionMode != nil {
		item.SessionMode = *request.SessionMode
	}
	if request.MaxConcurrentRuns != nil {
		item.MaxConcurrentRuns = *request.MaxConcurrentRuns
	}
	if item.MaxConcurrentRuns == 0 {
		item.MaxConcurrentRuns = 1
	}
	if _, err := s.store.GetRuntimeConnection(r.Context(), item.RuntimeConnectionID); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.UpdateAgentContext(r.Context(), item); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteAgentContext(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAgentContext(r.Context(), r.PathValue("context")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
