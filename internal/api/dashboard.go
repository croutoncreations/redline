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

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/usage"
)

//go:embed dashboard/*
var dashboardFiles embed.FS

type dashboardResponse struct {
	GeneratedAt  time.Time                `json:"generated_at"`
	ActivePolicy string                   `json:"active_policy"`
	Policies     map[string]config.Policy `json:"policies"`
	Health       domain.OperationalHealth `json:"health"`
	Scheduler    scheduler.Status         `json:"scheduler"`
	UsageMonitor scheduler.Status         `json:"usage_monitor"`
	Providers    []dashboardProvider      `json:"providers"`
	Tasks        []dashboardTask          `json:"tasks"`
	Runs         []domain.Run             `json:"runs"`
	Attempts     []domain.DispatchAttempt `json:"attempts"`
	UnreadRuns   int                      `json:"unread_runs"`
	Demo         *dashboardDemo           `json:"demo,omitempty"`
}

type dashboardDemo struct {
	Scenario  string `json:"scenario"`
	Synthetic bool   `json:"synthetic"`
}

type dashboardProvider struct {
	ID                       string                  `json:"id"`
	Provider                 string                  `json:"provider"`
	Paused                   bool                    `json:"paused"`
	Snapshot                 *decision.UsageSnapshot `json:"snapshot,omitempty"`
	SnapshotStale            bool                    `json:"snapshot_stale"`
	Error                    string                  `json:"error,omitempty"`
	UsageSource              usage.Status            `json:"usage_source"`
	Policy                   string                  `json:"policy"`
	PolicySource             string                  `json:"policy_source"`
	DefaultPolicy            string                  `json:"default_policy"`
	MaxConcurrentRuns        int                     `json:"max_concurrent_runs"`
	DefaultMaxConcurrentRuns int                     `json:"default_max_concurrent_runs"`
	ConcurrencySource        string                  `json:"concurrency_source"`
	ActiveRuns               int                     `json:"active_runs"`
	PoolConcurrency          map[string]int          `json:"pool_concurrency,omitempty"`
	ActivePoolClaims         map[string]int          `json:"active_pool_claims,omitempty"`
	LatestDecision           *dashboardDecision      `json:"latest_decision,omitempty"`
	LatestDecisionAt         *time.Time              `json:"latest_decision_at,omitempty"`
}

type dashboardDecision struct {
	Decision            decision.Decision   `json:"decision"`
	Mode                decision.Mode       `json:"mode"`
	Reason              string              `json:"reason"`
	Overflow            float64             `json:"overflow"`
	RollingDispatchable float64             `json:"rolling_dispatchable"`
	PaceGap             float64             `json:"pace_gap"`
	UnlockedTier        domain.DispatchTier `json:"unlocked_tier,omitempty"`
	ProjectedTriggerAt  *time.Time          `json:"projected_trigger_at,omitempty"`
	ProjectionBasis     string              `json:"projection_basis,omitempty"`
}

// dashboardTask intentionally excludes prompts and harness commands. The dashboard is
// operational telemetry, not a second task-definition API.
type dashboardTask struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Priority           int                 `json:"priority"`
	Type               domain.TaskType     `json:"type"`
	State              domain.TaskState    `json:"state"`
	Enabled            bool                `json:"enabled"`
	ExecutionProfileID string              `json:"execution_profile_id"`
	ProviderAccountID  string              `json:"provider_account_id"`
	HarnessType        string              `json:"harness_type"`
	Model              string              `json:"model,omitempty"`
	WorkspaceProvider  string              `json:"workspace_provider"`
	MinInterval        time.Duration       `json:"min_interval"`
	RequireRepoChange  bool                `json:"require_repo_change"`
	DispatchTier       domain.DispatchTier `json:"dispatch_tier"`
	LastStartedAt      *time.Time          `json:"last_started_at,omitempty"`
	LastCompletedAt    *time.Time          `json:"last_completed_at,omitempty"`
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/m" {
		s.serveDashboardFile(w, "dashboard/mobile.html", "text/html; charset=utf-8")
		return
	}
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

func (s *Server) mobileServiceWorker(w http.ResponseWriter, _ *http.Request) {
	s.serveDashboardFile(w, "dashboard/sw.js", "text/javascript; charset=utf-8")
}

func (s *Server) mobileDashboardAsset(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("asset") {
	case "mobile.css":
		s.serveDashboardFile(w, "dashboard/mobile.css", "text/css; charset=utf-8")
	case "mobile.js":
		s.serveDashboardFile(w, "dashboard/mobile.js", "text/javascript; charset=utf-8")
	case "manifest.webmanifest":
		s.serveDashboardFile(w, "dashboard/manifest.webmanifest", "application/manifest+json")
	case "icon-192.png":
		s.serveDashboardFile(w, "dashboard/icon-192.png", "image/png")
	case "icon-512.png":
		s.serveDashboardFile(w, "dashboard/icon-512.png", "image/png")
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
	maxSnapshotAge, err := s.config.SnapshotAge()
	if err != nil {
		return dashboardResponse{}, err
	}
	result := dashboardResponse{
		GeneratedAt: s.now(), ActivePolicy: s.config.ActivePolicy,
		Policies:  s.config.Policies,
		Scheduler: s.scheduler.Status(), UsageMonitor: s.usageMonitor.Status(),
		Providers: make([]dashboardProvider, 0, len(s.config.Providers)),
		Tasks:     make([]dashboardTask, 0), Runs: make([]domain.Run, 0), Attempts: make([]domain.DispatchAttempt, 0),
	}
	if s.config.DemoScenario != "" {
		result.Demo = &dashboardDemo{Scenario: s.config.DemoScenario, Synthetic: true}
		if result.Scheduler.Enabled && result.Scheduler.NextCycleAt == nil {
			interval, _ := s.config.SchedulerInterval()
			next := s.now().UTC().Add(interval)
			result.Scheduler.NextCycleAt = &next
		}
	}
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
		item := dashboardProvider{ID: id, Provider: configured.Provider, UsageSource: s.usageSources.Status(id)}
		selection, selectionErr := s.effectiveProviderPolicy(ctx, id)
		if selectionErr != nil {
			return dashboardResponse{}, selectionErr
		}
		item.Policy, item.PolicySource = selection.Policy, selection.Source
		item.DefaultPolicy = configured.Policy
		if item.DefaultPolicy == "" {
			item.DefaultPolicy = s.config.ActivePolicy
		}
		concurrency, concurrencyErr := s.effectiveProviderConcurrency(ctx, id)
		if concurrencyErr != nil {
			return dashboardResponse{}, concurrencyErr
		}
		item.MaxConcurrentRuns = concurrency.MaxConcurrentRuns
		item.DefaultMaxConcurrentRuns = concurrency.DefaultMaxConcurrentRuns
		item.ConcurrencySource = concurrency.Source
		item.PoolConcurrency = configured.PoolConcurrency
		item.ActiveRuns, err = s.store.ActiveRunCount(ctx, id)
		if err != nil {
			return dashboardResponse{}, err
		}
		if len(configured.PoolConcurrency) > 0 {
			item.ActivePoolClaims = make(map[string]int, len(configured.PoolConcurrency))
			for pool := range configured.PoolConcurrency {
				item.ActivePoolClaims[pool], err = s.store.ActivePoolClaimCount(ctx, id, pool)
				if err != nil {
					return dashboardResponse{}, err
				}
			}
		}
		item.Paused, err = s.store.ProviderPaused(ctx, id)
		if err != nil {
			return dashboardResponse{}, err
		}
		snapshot, _, snapshotErr := s.store.LatestSnapshotFromSource(ctx, configured.Provider, item.UsageSource.Active)
		if snapshotErr != nil {
			if errors.Is(snapshotErr, store.ErrNotFound) {
				item.Error = "No usage snapshot has been collected yet."
			} else {
				item.Error = snapshotErr.Error()
			}
		} else {
			item.Snapshot = &snapshot
			age := s.now().Sub(snapshot.ObservedAt)
			if age > maxSnapshotAge || age < 0 {
				item.SnapshotStale = true
				item.Error = "Usage data is stale; scheduling is paused until a fresh snapshot is available."
			}
		}
		attempts, attemptsErr := s.store.ListDispatchAttempts(ctx, id, 8)
		if attemptsErr != nil {
			return dashboardResponse{}, attemptsErr
		}
		result.Attempts = append(result.Attempts, attempts...)
		decisions, decisionsErr := s.store.ListSchedulerDecisions(ctx, id, 1)
		if decisionsErr != nil {
			return dashboardResponse{}, decisionsErr
		}
		if len(decisions) > 0 {
			var latest decisionResponse
			if unmarshalErr := json.Unmarshal(decisions[0].DecisionJSON, &latest); unmarshalErr == nil {
				item.LatestDecision = &dashboardDecision{
					Decision: latest.Result.Decision, Mode: latest.Result.Mode, Reason: latest.Result.Reason,
					Overflow: latest.Result.Overflow, RollingDispatchable: latest.Result.RollingDispatchable,
					PaceGap: latest.Result.PaceGap, UnlockedTier: latest.Result.UnlockedTier,
				}
				if !item.SnapshotStale {
					projected, projectionErr := s.projectedTrigger(ctx, id, snapshot, selection)
					if projectionErr == nil {
						item.LatestDecision.ProjectedTriggerAt = projected
						item.LatestDecision.ProjectionBasis = "Assumes weekly usage stays unchanged."
					}
				}
				createdAt := decisions[0].CreatedAt
				item.LatestDecisionAt = &createdAt
			}
		}
		result.Providers = append(result.Providers, item)
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
			DispatchTier:  task.DispatchTier,
			LastStartedAt: task.LastStartedAt, LastCompletedAt: task.LastCompletedAt,
		})
	}
	result.Runs, err = s.store.ListRuns(ctx, 20)
	if err != nil {
		return dashboardResponse{}, err
	}
	result.UnreadRuns, err = s.store.UnreadRunActivityCount(ctx)
	if err != nil {
		return dashboardResponse{}, err
	}
	return result, nil
}

func (s *Server) projectedTrigger(
	ctx context.Context,
	providerID string,
	snapshot decision.UsageSnapshot,
	selection providerPolicySelection,
) (*time.Time, error) {
	thresholds, err := selection.Definition.DecisionThresholds()
	if err != nil {
		return nil, err
	}
	maxAge, err := s.config.SnapshotAge()
	if err != nil {
		return nil, err
	}
	estimate, err := s.calibration(ctx, providerID)
	if err != nil {
		return nil, err
	}
	pollInterval, err := s.config.SchedulerInterval()
	if err != nil {
		return nil, err
	}
	return decision.ProjectTriggerAt(decision.Input{
		Snapshot: snapshot, WindowWeeklyCost: estimate.EffectiveCost,
		WindowWeeklyCostSource: string(estimate.Source), CalibrationConfidence: string(estimate.Confidence),
		TriggerMargin: selection.Definition.TriggerMargin, RollingReserve: selection.Definition.RollingReserve,
		PaceGapTrigger: selection.Definition.PaceGapTrigger,
		PaceThresholds: thresholds, Now: s.now(), MaxSnapshotAge: maxAge,
	}, pollInterval), nil
}
