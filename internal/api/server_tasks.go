package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/tasktemplate"
)

func (s *Server) listTaskTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, tasktemplate.Catalog())
}

type taskRequest struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Prompt             string              `json:"prompt"`
	PromptFile         string              `json:"prompt_file"`
	Priority           int                 `json:"priority"`
	ExecutionProfileID string              `json:"execution_profile_id"`
	RuntimeJobID       string              `json:"runtime_job_id"`
	Type               domain.TaskType     `json:"type"`
	MinInterval        string              `json:"min_interval"`
	RequireRepoChange  bool                `json:"require_repo_change"`
	DispatchTier       domain.DispatchTier `json:"dispatch_tier"`
}

type taskUpdateRequest struct {
	Name               *string              `json:"name"`
	Prompt             *string              `json:"prompt"`
	PromptFile         *string              `json:"prompt_file"`
	Priority           *int                 `json:"priority"`
	ExecutionProfileID *string              `json:"execution_profile_id"`
	RuntimeJobID       *string              `json:"runtime_job_id"`
	Type               *domain.TaskType     `json:"type"`
	MinInterval        *string              `json:"min_interval"`
	RequireRepoChange  *bool                `json:"require_repo_change"`
	DispatchTier       *domain.DispatchTier `json:"dispatch_tier"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var request taskRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	interval := time.Duration(0)
	var err error
	if request.MinInterval != "" {
		interval, err = parseDuration(request.MinInterval)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, problem{Error: "invalid min_interval"})
			return
		}
	}
	id := request.ID
	if id == "" {
		id = uuid.NewString()
	}
	task := domain.Task{
		ID: id, Name: request.Name, Prompt: request.Prompt, PromptFile: request.PromptFile,
		Priority: request.Priority, ExecutionProfileID: request.ExecutionProfileID,
		RuntimeJobID: request.RuntimeJobID,
		Type:         request.Type, MinInterval: interval, RequireRepoChange: request.RequireRepoChange, DispatchTier: request.DispatchTier,
	}
	if err := s.store.CreateTask(r.Context(), task, s.now()); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.store.ListTasks(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.GetTask(r.Context(), r.PathValue("task"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	var request taskUpdateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	task, err := s.store.GetTask(r.Context(), r.PathValue("task"))
	if err != nil {
		writeError(w, err)
		return
	}
	if request.Name != nil {
		task.Name = *request.Name
	}
	if request.Prompt != nil {
		task.Prompt = *request.Prompt
	}
	if request.PromptFile != nil {
		task.PromptFile = *request.PromptFile
	}
	if request.Priority != nil {
		task.Priority = *request.Priority
	}
	if request.ExecutionProfileID != nil {
		task.ExecutionProfileID = *request.ExecutionProfileID
	}
	if request.RuntimeJobID != nil {
		task.RuntimeJobID = *request.RuntimeJobID
	}
	if request.Type != nil {
		task.Type = *request.Type
	}
	if request.MinInterval != nil {
		task.MinInterval = 0
		if *request.MinInterval != "" {
			task.MinInterval, err = parseDuration(*request.MinInterval)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, problem{Error: "invalid min_interval"})
				return
			}
		}
	}
	if request.RequireRepoChange != nil {
		task.RequireRepoChange = *request.RequireRepoChange
	}
	if request.DispatchTier != nil {
		task.DispatchTier = *request.DispatchTier
	}
	if _, err := s.store.GetProfile(r.Context(), task.ExecutionProfileID); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "execution_profile_id is not configured"})
		return
	}
	if err := s.store.UpdateTask(r.Context(), task, s.now()); err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.GetTask(r.Context(), task.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTask(r.Context(), r.PathValue("task")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) taskControl(w http.ResponseWriter, r *http.Request) {
	control := r.PathValue("control")
	if err := s.store.SetTaskControl(r.Context(), r.PathValue("task"), control, s.now()); err != nil {
		writeError(w, err)
		return
	}
	task, err := s.store.GetTask(r.Context(), r.PathValue("task"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}
