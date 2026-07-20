package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
)

//go:embed dashboard/*
var dashboardFiles embed.FS

type dashboardResponse struct {
	GeneratedAt  time.Time                `json:"generated_at"`
	ActivePolicy string                   `json:"active_policy"`
	Health       domain.OperationalHealth `json:"health"`
	Scheduler    scheduler.Status         `json:"scheduler"`
	UsageMonitor scheduler.Status         `json:"usage_monitor"`
	Providers    []dashboardProvider      `json:"providers"`
	Tasks        []dashboardTask          `json:"tasks"`
	Runs         []domain.Run             `json:"runs"`
	Attempts     []domain.DispatchAttempt `json:"attempts"`
}

type dashboardProvider struct {
	ID       string                  `json:"id"`
	Provider string                  `json:"provider"`
	Snapshot *decision.UsageSnapshot `json:"snapshot,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

// dashboardTask intentionally excludes prompts and harness commands. The dashboard is
// operational telemetry, not a second task-definition API.
type dashboardTask struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Priority           int              `json:"priority"`
	Type               domain.TaskType  `json:"type"`
	State              domain.TaskState `json:"state"`
	Enabled            bool             `json:"enabled"`
	ExecutionProfileID string           `json:"execution_profile_id"`
	ProviderAccountID  string           `json:"provider_account_id"`
	HarnessType        string           `json:"harness_type"`
	Model              string           `json:"model,omitempty"`
	WorkspaceProvider  string           `json:"workspace_provider"`
	MinInterval        time.Duration    `json:"min_interval"`
	RequireRepoChange  bool             `json:"require_repo_change"`
	LastStartedAt      *time.Time       `json:"last_started_at,omitempty"`
	LastCompletedAt    *time.Time       `json:"last_completed_at,omitempty"`
}

func (s *Server) dashboardPage(w http.ResponseWriter, _ *http.Request) {
	s.serveDashboardFile(w, "dashboard/index.html", "text/html; charset=utf-8")
}

func (s *Server) dashboardAsset(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("asset") {
	case "dashboard.css":
		s.serveDashboardFile(w, "dashboard/dashboard.css", "text/css; charset=utf-8")
	case "dashboard.js":
		s.serveDashboardFile(w, "dashboard/dashboard.js", "text/javascript; charset=utf-8")
	case "claude.svg":
		s.serveDashboardFile(w, "dashboard/claude.svg", "image/svg+xml")
	case "codex.svg":
		s.serveDashboardFile(w, "dashboard/codex.svg", "image/svg+xml")
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveDashboardFile(w http.ResponseWriter, name, contentType string) {
	contents, err := dashboardFiles.ReadFile(name)
	if err != nil {
		http.Error(w, "dashboard asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(contents)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	result, err := s.dashboardData(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) dashboardEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, problem{Error: "streaming is unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func() error {
		result, err := s.dashboardData(r.Context())
		if err != nil {
			return err
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: dashboard\ndata: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if err := send(); err != nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := send(); err != nil {
				return
			}
		}
	}
}

func (s *Server) dashboardData(ctx context.Context) (dashboardResponse, error) {
	result := dashboardResponse{
		GeneratedAt: s.now(), ActivePolicy: s.config.ActivePolicy,
		Scheduler: s.scheduler.Status(), UsageMonitor: s.usageMonitor.Status(),
		Providers: make([]dashboardProvider, 0, len(s.config.Providers)),
		Tasks:     make([]dashboardTask, 0), Runs: make([]domain.Run, 0), Attempts: make([]domain.DispatchAttempt, 0),
	}
	var err error
	result.Health, err = s.store.OperationalHealth(ctx, s.now(), 24*time.Hour)
	if err != nil {
		return dashboardResponse{}, err
	}

	providerIDs := make([]string, 0, len(s.config.Providers))
	for id := range s.config.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, id := range providerIDs {
		configured := s.config.Providers[id]
		item := dashboardProvider{ID: id, Provider: configured.Provider}
		snapshot, _, snapshotErr := s.store.LatestSnapshot(ctx, configured.Provider)
		if snapshotErr != nil {
			if errors.Is(snapshotErr, store.ErrNotFound) {
				item.Error = "No usage snapshot has been collected yet."
			} else {
				item.Error = snapshotErr.Error()
			}
		} else {
			item.Snapshot = &snapshot
		}
		result.Providers = append(result.Providers, item)

		attempts, attemptsErr := s.store.ListDispatchAttempts(ctx, id, 8)
		if attemptsErr != nil {
			return dashboardResponse{}, attemptsErr
		}
		result.Attempts = append(result.Attempts, attempts...)
	}
	sort.Slice(result.Attempts, func(i, j int) bool { return result.Attempts[i].CompletedAt.After(result.Attempts[j].CompletedAt) })

	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return dashboardResponse{}, err
	}
	for _, task := range tasks {
		profile, profileErr := s.store.GetProfile(ctx, task.ExecutionProfileID)
		if profileErr != nil {
			return dashboardResponse{}, profileErr
		}
		result.Tasks = append(result.Tasks, dashboardTask{
			ID: task.ID, Name: task.Name, Priority: task.Priority, Type: task.Type, State: task.State, Enabled: task.Enabled,
			ExecutionProfileID: task.ExecutionProfileID, ProviderAccountID: profile.ProviderAccountID,
			HarnessType: profile.HarnessType, Model: profile.Model, WorkspaceProvider: profile.WorkspaceProvider,
			MinInterval: task.MinInterval, RequireRepoChange: task.RequireRepoChange,
			LastStartedAt: task.LastStartedAt, LastCompletedAt: task.LastCompletedAt,
		})
	}
	result.Runs, err = s.store.ListRuns(ctx, 20)
	if err != nil {
		return dashboardResponse{}, err
	}
	return result, nil
}
