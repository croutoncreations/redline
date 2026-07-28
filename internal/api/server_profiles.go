package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jfox/redline/internal/domain"
)

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var profile domain.ExecutionProfile
	if err := decodeJSON(r, &profile); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if profile.ID == "" {
		profile.ID = uuid.NewString()
	}
	if _, ok := s.config.Providers[profile.ProviderAccountID]; !ok {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider_account_id is not configured"})
		return
	}
	if err := s.validateProfileRuntime(r.Context(), profile); err != nil {
		writeError(w, err)
		return
	}
	profile.CreatedAt = s.now().UTC()
	if err := s.store.CreateProfile(r.Context(), profile, profile.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

type profileUpdateRequest struct {
	ProviderAccountID *string   `json:"provider_account_id"`
	AgentContextID    *string   `json:"agent_context_id"`
	HarnessType       *string   `json:"harness_type"`
	Model             *string   `json:"model"`
	BudgetModelGroup  *string   `json:"budget_model_group"`
	HarnessCommand    *string   `json:"harness_command"`
	HarnessArgs       *[]string `json:"harness_args"`
	WorkspaceProvider *string   `json:"workspace_provider"`
	WorkspaceArgs     *[]string `json:"workspace_args"`
	Repository        *string   `json:"repository"`
	BaseBranch        *string   `json:"base_branch"`
	RequireClean      *bool     `json:"require_clean"`
	CleanupPolicy     *string   `json:"cleanup_policy"`
	PrepareCommand    *string   `json:"prepare_command"`
	FinalizeCommand   *string   `json:"finalize_command"`
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := s.store.GetProfile(r.Context(), r.PathValue("profile"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var request profileUpdateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	profile, err := s.store.GetProfile(r.Context(), r.PathValue("profile"))
	if err != nil {
		writeError(w, err)
		return
	}
	if request.ProviderAccountID != nil {
		profile.ProviderAccountID = *request.ProviderAccountID
	}
	if request.AgentContextID != nil {
		profile.AgentContextID = *request.AgentContextID
	}
	if request.HarnessType != nil {
		profile.HarnessType = *request.HarnessType
	}
	if request.Model != nil {
		profile.Model = *request.Model
	}
	if request.BudgetModelGroup != nil {
		profile.BudgetModelGroup = *request.BudgetModelGroup
	}
	if request.HarnessCommand != nil {
		profile.HarnessCommand = *request.HarnessCommand
	}
	if request.HarnessArgs != nil {
		profile.HarnessArgs = *request.HarnessArgs
	}
	if request.WorkspaceProvider != nil {
		profile.WorkspaceProvider = *request.WorkspaceProvider
	}
	if request.WorkspaceArgs != nil {
		profile.WorkspaceArgs = *request.WorkspaceArgs
	}
	if request.Repository != nil {
		profile.Repository = *request.Repository
	}
	if request.BaseBranch != nil {
		profile.BaseBranch = *request.BaseBranch
	}
	if request.RequireClean != nil {
		profile.RequireClean = *request.RequireClean
	}
	if request.CleanupPolicy != nil {
		profile.CleanupPolicy = *request.CleanupPolicy
	}
	if request.PrepareCommand != nil {
		profile.PrepareCommand = *request.PrepareCommand
	}
	if request.FinalizeCommand != nil {
		profile.FinalizeCommand = *request.FinalizeCommand
	}
	if _, ok := s.config.Providers[profile.ProviderAccountID]; !ok {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider_account_id is not configured"})
		return
	}
	if err := s.validateProfileRuntime(r.Context(), profile); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.UpdateProfile(r.Context(), profile); err != nil {
		writeError(w, err)
		return
	}
	updated, err := s.store.GetProfile(r.Context(), profile.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) validateProfileRuntime(ctx context.Context, profile domain.ExecutionProfile) error {
	if profile.HarnessType == "hermes" && profile.AgentContextID == "" {
		return fmt.Errorf("agent_context_id is required for a Hermes execution profile")
	}
	if profile.AgentContextID == "" {
		return nil
	}
	agentContext, err := s.store.GetAgentContext(ctx, profile.AgentContextID)
	if err != nil {
		return err
	}
	connection, err := s.store.GetRuntimeConnection(ctx, agentContext.RuntimeConnectionID)
	if err != nil {
		return err
	}
	if profile.HarnessType == "hermes" && connection.Runtime != "hermes" {
		return fmt.Errorf("a Hermes runtime connection is required for a Hermes execution profile")
	}
	return nil
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteProfile(r.Context(), r.PathValue("profile")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.store.ListProfiles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}
