package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/discovery"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/execution"
	"github.com/jfox/redline/internal/harness"
	"github.com/jfox/redline/internal/nativeusage"
	"github.com/jfox/redline/internal/notification"
	"github.com/jfox/redline/internal/openusage"
	autoscheduler "github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/tokenlog"
	"github.com/jfox/redline/internal/usage"
	"github.com/jfox/redline/internal/workspace"
)

type Executor interface {
	Execute(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error
}

type Notifier interface {
	Notify(context.Context, domain.NotificationEvent) error
}

type HarnessDiscoverer interface {
	Discover(context.Context) discovery.Catalog
}

type Server struct {
	config       config.Config
	store        *store.DB
	now          func() time.Time
	executor     Executor
	notifier     Notifier
	revision     workspace.RevisionResolver
	artifacts    artifacts.Reader
	scheduler    *autoscheduler.Loop
	usageMonitor *autoscheduler.Loop
	discovery    HarnessDiscoverer
	usageSources *usage.Manager
	catalogMu    sync.Mutex
	catalog      discovery.Catalog
	catalogAt    time.Time
	mux          *http.ServeMux
	workers      sync.WaitGroup
	loopWorkers  sync.WaitGroup
	startMu      sync.Mutex
	started      bool
}

func NewServer(cfg config.Config, database *store.DB, now func() time.Time) *Server {
	notifier := configuredNotifier(cfg, database, now)
	defaultExecutor := execution.Executor{
		Store: database, Workspaces: workspace.Manager{OutputDirectory: cfg.ArtifactsDirectory()}, Harness: &harness.Adapter{},
		Notifier: notifier, UsageRecorder: tokenlog.RunUsageRecorder{Store: database},
		OutputDirectory: cfg.ArtifactsDirectory(), Now: now,
	}
	return newServer(cfg, database, now, defaultExecutor, workspace.GitRevisionResolver{}, notifier)
}

func NewServerWithHarnessDiscoverer(cfg config.Config, database *store.DB, now func() time.Time, discoverer HarnessDiscoverer) *Server {
	server := NewServer(cfg, database, now)
	server.discovery = discoverer
	return server
}

func NewServerWithExecutor(
	cfg config.Config,
	database *store.DB,
	now func() time.Time,
	executor Executor,
) *Server {
	return NewServerWithDependencies(cfg, database, now, executor, workspace.GitRevisionResolver{})
}

func NewServerWithDependencies(
	cfg config.Config,
	database *store.DB,
	now func() time.Time,
	executor Executor,
	revision workspace.RevisionResolver,
) *Server {
	return newServer(cfg, database, now, executor, revision, configuredNotifier(cfg, database, now))
}

func newServer(
	cfg config.Config,
	database *store.DB,
	now func() time.Time,
	executor Executor,
	revision workspace.RevisionResolver,
	notifier Notifier,
) *Server {
	server := &Server{
		config: cfg, store: database, now: now, executor: executor, revision: revision, notifier: notifier,
		artifacts: artifacts.Reader{Root: cfg.ArtifactsDirectory()},
		discovery: discovery.Service{Now: now},
	}
	server.usageSources = usage.NewManager(
		openusage.Source{},
		nativeusage.Client{Credentials: nativeusage.NewDefaultCredentials(), Now: now},
		now,
	)
	interval, _ := cfg.SchedulerInterval()
	providers := make([]string, 0, len(cfg.Providers))
	for provider := range cfg.Providers {
		providers = append(providers, provider)
	}
	server.scheduler = autoscheduler.NewLoop(cfg.Scheduler.Enabled, interval, providers, server.dispatchAutomatic)
	monitorInterval, _ := cfg.UsageMonitorInterval()
	server.usageMonitor = autoscheduler.NewLoop(cfg.UsageMonitor.Enabled, monitorInterval, providers, server.monitorProvider)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", server.health)
	mux.HandleFunc("GET /v1/health/details", server.healthDetails)
	mux.HandleFunc("GET /v1/dashboard", server.dashboard)
	mux.HandleFunc("GET /v1/dashboard/events", server.dashboardEvents)
	mux.HandleFunc("POST /v1/providers/{provider}/refresh", server.refresh)
	mux.HandleFunc("GET /v1/providers/{provider}/status", server.status)
	mux.HandleFunc("GET /v1/providers/{provider}/calibration", server.providerCalibration)
	mux.HandleFunc("GET /v1/providers/{provider}/capacity", server.providerCapacity)
	mux.HandleFunc("POST /v1/providers/{provider}/token-sync", server.syncProviderTokens)
	mux.HandleFunc("POST /v1/providers/{provider}/decision", server.providerDecision)
	mux.HandleFunc("PATCH /v1/providers/{provider}/policy", server.updateProviderPolicy)
	mux.HandleFunc("POST /v1/providers/{provider}/{control}", server.providerControl)
	mux.HandleFunc("GET /v1/profiles", server.listProfiles)
	mux.HandleFunc("GET /v1/profile-options", server.profileOptions)
	mux.HandleFunc("POST /v1/profiles", server.createProfile)
	mux.HandleFunc("GET /v1/profiles/{profile}", server.getProfile)
	mux.HandleFunc("PATCH /v1/profiles/{profile}", server.updateProfile)
	mux.HandleFunc("DELETE /v1/profiles/{profile}", server.deleteProfile)
	mux.HandleFunc("GET /v1/tasks", server.listTasks)
	mux.HandleFunc("POST /v1/tasks", server.createTask)
	mux.HandleFunc("GET /v1/tasks/{task}", server.getTask)
	mux.HandleFunc("PATCH /v1/tasks/{task}", server.updateTask)
	mux.HandleFunc("DELETE /v1/tasks/{task}", server.deleteTask)
	mux.HandleFunc("POST /v1/tasks/{task}/{control}", server.taskControl)
	mux.HandleFunc("POST /v1/scheduler/evaluate", server.evaluateScheduler)
	mux.HandleFunc("POST /v1/scheduler/execute", server.executeScheduler)
	mux.HandleFunc("GET /v1/scheduler/status", server.schedulerStatus)
	mux.HandleFunc("GET /v1/usage-monitor/status", server.usageMonitorStatus)
	mux.HandleFunc("GET /v1/scheduler/decisions", server.listDecisions)
	mux.HandleFunc("GET /v1/scheduler/attempts", server.listAttempts)
	mux.HandleFunc("GET /v1/runs", server.listRuns)
	mux.HandleFunc("GET /v1/runs/{run}/events", server.listRunEvents)
	mux.HandleFunc("GET /v1/runs/{run}/logs", server.getRunLogs)
	mux.HandleFunc("GET /v1/runs/{run}", server.getRun)
	mux.HandleFunc("GET /v1/notifications", server.listNotifications)
	mux.HandleFunc("GET /{$}", server.dashboardPage)
	mux.HandleFunc("GET /dashboard", server.dashboardPage)
	mux.HandleFunc("GET /assets/{asset}", server.dashboardAsset)
	server.mux = mux
	return server
}

func configuredNotifier(cfg config.Config, database *store.DB, now func() time.Time) notification.Service {
	return notification.Service{
		Enabled: cfg.Notifications.Enabled, Events: cfg.NotificationEvents(), Store: database,
		Sink:    notification.CommandSink{Command: cfg.Notifications.Command},
		Timeout: cfg.NotificationTimeout(), Now: now,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) StartScheduler(ctx context.Context) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started {
		return
	}
	s.started = true
	s.loopWorkers.Add(1)
	go func() {
		defer s.loopWorkers.Done()
		s.scheduler.Run(ctx)
	}()
	s.loopWorkers.Add(1)
	go func() {
		defer s.loopWorkers.Done()
		s.usageMonitor.Run(ctx)
	}()
}

func (s *Server) Wait() {
	s.loopWorkers.Wait()
	s.workers.Wait()
}

func (s *Server) schedulerStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Status())
}

func (s *Server) usageMonitorStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.usageMonitor.Status())
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) healthDetails(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	var err error
	if configured := r.URL.Query().Get("window"); configured != "" {
		window, err = parseDuration(configured)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, problem{Error: "window must be a positive duration"})
			return
		}
	}
	health, err := s.store.OperationalHealth(r.Context(), s.now(), window)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, health)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	deliveries, err := s.store.ListNotificationDeliveries(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveries)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	snapshot, _, err := s.fetchAndStore(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	configured, ok := s.config.Providers[r.PathValue("provider")]
	if !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	snapshot, _, err := s.store.LatestSnapshotFromSource(r.Context(), configured.Provider, s.usageSources.Status(r.PathValue("provider")).Active)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) providerCalibration(w http.ResponseWriter, r *http.Request) {
	estimate, err := s.calibration(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, estimate)
}

func (s *Server) providerCapacity(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider")
	configured, ok := s.config.Providers[providerID]
	if !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: fmt.Sprintf("provider %q is not configured", providerID)})
		return
	}
	snapshots, err := s.store.ListSnapshots(r.Context(), configured.Provider, 5000)
	if err != nil {
		writeError(w, err)
		return
	}
	var after time.Time
	if len(snapshots) > 0 {
		after = snapshots[0].ObservedAt.Add(-time.Nanosecond)
	}
	observations, err := s.store.ListTokenObservations(r.Context(), configured.Provider, after, time.Time{})
	if err != nil {
		writeError(w, err)
		return
	}
	calibrated, err := s.calibration(r.Context(), providerID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, capacity.Estimate(configured.Provider, snapshots, observations, calibrated.EffectiveCost, s.now()))
}

type tokenSyncResult struct {
	Provider          string    `json:"provider"`
	Read              int       `json:"read"`
	Inserted          int       `json:"inserted"`
	OwnedRunsInserted int       `json:"owned_runs_inserted,omitempty"`
	Cursor            time.Time `json:"cursor,omitempty"`
}

func (s *Server) syncProviderTokens(w http.ResponseWriter, r *http.Request) {
	result, err := s.syncTokens(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) syncTokens(ctx context.Context, providerID string) (tokenSyncResult, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return tokenSyncResult{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	ownedInserted, err := s.syncOwnedRunTokens(ctx, providerID)
	if err != nil {
		return tokenSyncResult{}, err
	}
	if strings.TrimSpace(s.config.UsageMonitor.GatepostDatabase) == "" {
		return tokenSyncResult{}, fmt.Errorf("usage_monitor gatepost_database is not configured")
	}
	directCursor, err := s.store.LatestTokenObservationTime(ctx, configured.Provider, "gatepost")
	if err != nil {
		return tokenSyncResult{}, err
	}
	// Re-read a small overlap so records sharing a timestamp with the cursor are
	// not missed when Gatepost appends to an active session. Stable source IDs
	// make the overlap idempotent.
	queryAfter := directCursor
	if !queryAfter.IsZero() {
		queryAfter = queryAfter.Add(-time.Minute)
	}
	observations, err := tokenlog.LoadGatepost(ctx, s.config.UsageMonitor.GatepostDatabase, configured.Provider, queryAfter)
	if err != nil {
		return tokenSyncResult{}, err
	}
	piCursor, err := s.store.LatestTokenObservationTime(ctx, configured.Provider, "gatepost-pi")
	if err != nil {
		return tokenSyncResult{}, err
	}
	piAfter := piCursor
	if !piAfter.IsZero() {
		piAfter = piAfter.Add(-time.Minute)
	}
	piObservations, err := tokenlog.LoadGatepostPi(ctx, s.config.UsageMonitor.GatepostDatabase, configured.Provider, piAfter)
	if err != nil {
		return tokenSyncResult{}, err
	}
	observations = append(observations, piObservations...)
	inserted, err := s.store.SaveTokenObservations(ctx, observations)
	if err != nil {
		return tokenSyncResult{}, err
	}
	latest := directCursor
	if piCursor.After(latest) {
		latest = piCursor
	}
	for _, observation := range observations {
		if observation.ObservedAt.After(latest) {
			latest = observation.ObservedAt
		}
	}
	return tokenSyncResult{
		Provider: configured.Provider, Read: len(observations), Inserted: inserted,
		OwnedRunsInserted: ownedInserted, Cursor: latest,
	}, nil
}

// syncOwnedRunTokens recovers usage from completed Redline runs. The stable
// redline-run source IDs make this safe to repeat on every monitor cycle and
// ensure a service restart cannot permanently lose an owned run's accounting.
func (s *Server) syncOwnedRunTokens(ctx context.Context, providerID string) (int, error) {
	runs, err := s.store.ListRuns(ctx, 1000)
	if err != nil {
		return 0, err
	}
	recorder := tokenlog.RunUsageRecorder{Store: s.store}
	inserted := 0
	for _, run := range runs {
		if run.ProviderAccountID != providerID || run.CompletedAt == nil || run.OutputFile == "" ||
			(run.State != domain.RunCompleted && run.State != domain.RunFailed) {
			continue
		}
		if _, err := os.Stat(run.OutputFile); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return inserted, fmt.Errorf("inspect run %q usage artifact: %w", run.ID, err)
		}
		task, err := s.store.GetTask(ctx, run.TaskID)
		if err != nil {
			return inserted, fmt.Errorf("load run %q task for usage recovery: %w", run.ID, err)
		}
		profile, err := s.store.GetProfile(ctx, task.ExecutionProfileID)
		if err != nil {
			return inserted, fmt.Errorf("load run %q profile for usage recovery: %w", run.ID, err)
		}
		count, err := recorder.RecordRunUsage(ctx, run, profile, run.OutputFile, *run.CompletedAt)
		if err != nil {
			return inserted, fmt.Errorf("recover run %q usage: %w", run.ID, err)
		}
		inserted += count
	}
	return inserted, nil
}

func (s *Server) monitorProvider(ctx context.Context, providerID string) error {
	_, _, usageErr := s.fetchAndStore(ctx, providerID)
	_, tokenErr := s.syncTokens(ctx, providerID)
	return errors.Join(usageErr, tokenErr)
}

func (s *Server) providerDecision(w http.ResponseWriter, r *http.Request) {
	snapshot, result, err := s.evaluateProvider(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decisionResponse{Snapshot: snapshot, Result: result})
}

func (s *Server) providerControl(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	control := r.PathValue("control")
	if control != "pause" && control != "resume" {
		writeJSON(w, http.StatusNotFound, problem{Error: "unknown provider control"})
		return
	}
	paused := control == "pause"
	if err := s.store.SetProviderPaused(r.Context(), provider, paused); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider_account_id": provider, "paused": paused})
}

type providerPolicySelection struct {
	Policy     string        `json:"policy"`
	Source     string        `json:"source"`
	Definition config.Policy `json:"-"`
}

func (s *Server) effectiveProviderPolicy(ctx context.Context, providerID string) (providerPolicySelection, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return providerPolicySelection{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	name, source := configured.Policy, "provider"
	override, err := s.store.ProviderPolicy(ctx, providerID)
	if err != nil {
		return providerPolicySelection{}, err
	}
	if override != "" {
		name, source = override, "override"
	} else if name == "" {
		name, source = s.config.ActivePolicy, "global"
	}
	policy, ok := s.config.Policies[name]
	if !ok {
		return providerPolicySelection{}, fmt.Errorf("policy %q is not configured", name)
	}
	return providerPolicySelection{Policy: name, Source: source, Definition: policy}, nil
}

func (s *Server) updateProviderPolicy(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	var request struct {
		Policy *string `json:"policy"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if request.Policy == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "policy is required"})
		return
	}
	name := strings.TrimSpace(*request.Policy)
	if name != "" {
		if _, ok := s.config.Policies[name]; !ok {
			writeJSON(w, http.StatusBadRequest, problem{Error: "policy is not configured"})
			return
		}
	}
	if err := s.store.SetProviderPolicy(r.Context(), provider, name); err != nil {
		writeError(w, err)
		return
	}
	selection, err := s.effectiveProviderPolicy(r.Context(), provider)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, selection)
}

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
	profile.CreatedAt = s.now().UTC()
	if err := s.store.CreateProfile(r.Context(), profile, profile.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
}

type profileUpdateRequest struct {
	ProviderAccountID *string   `json:"provider_account_id"`
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

type taskRequest struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Prompt             string              `json:"prompt"`
	PromptFile         string              `json:"prompt_file"`
	Priority           int                 `json:"priority"`
	ExecutionProfileID string              `json:"execution_profile_id"`
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
		Type: request.Type, MinInterval: interval, RequireRepoChange: request.RequireRepoChange, DispatchTier: request.DispatchTier,
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

type schedulerRequest struct {
	ProviderAccountID string `json:"provider_account_id"`
	CurrentRevision   string `json:"current_revision"`
}

type schedulerResponse struct {
	Snapshot     decision.UsageSnapshot `json:"snapshot"`
	Result       decision.Result        `json:"result"`
	Trigger      string                 `json:"trigger,omitempty"`
	SelectedTask *domain.Task           `json:"selected_task,omitempty"`
	Run          *domain.Run            `json:"run,omitempty"`
}

func (s *Server) executeScheduler(w http.ResponseWriter, r *http.Request) {
	var request schedulerRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if request.ProviderAccountID == "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider_account_id is required"})
		return
	}
	response, admitted, err := s.dispatch(r.Context(), request, "manual")
	if err != nil {
		writeError(w, err)
		return
	}
	if response.Result.Mode == decision.ModePaused {
		writeJSON(w, http.StatusConflict, problem{Error: "provider is paused"})
		return
	}
	status := http.StatusOK
	if admitted {
		status = http.StatusAccepted
	}
	writeJSON(w, status, response)
}

func (s *Server) dispatchAutomatic(ctx context.Context, provider string) error {
	configured := s.config.Providers[provider]
	for range configured.EffectiveMaxConcurrentRuns() {
		_, admitted, err := s.dispatch(ctx, schedulerRequest{ProviderAccountID: provider}, "automatic")
		if err != nil {
			return err
		}
		if !admitted {
			return nil
		}
	}
	return nil
}

func (s *Server) dispatch(
	ctx context.Context,
	request schedulerRequest,
	trigger string,
) (response schedulerResponse, admitted bool, dispatchErr error) {
	startedAt := s.now().UTC()
	response, admitted, dispatchErr = s.dispatchCore(ctx, request, trigger)
	attempt := domain.DispatchAttempt{
		ProviderAccountID: request.ProviderAccountID, Trigger: trigger,
		StartedAt: startedAt, CompletedAt: s.now().UTC(),
		Decision: string(response.Result.Decision), Mode: string(response.Result.Mode),
		Reason: response.Result.Reason,
	}
	switch {
	case dispatchErr != nil:
		attempt.Outcome = domain.DispatchError
		attempt.Error = dispatchErr.Error()
	case admitted:
		attempt.Outcome = domain.DispatchAdmitted
	case response.Result.Decision == decision.Run:
		attempt.Outcome = domain.DispatchNoTask
		if response.Result.TaskSelectionReason != "" {
			attempt.Reason = response.Result.TaskSelectionReason
		}
	default:
		attempt.Outcome = domain.DispatchWait
	}
	if response.SelectedTask != nil {
		attempt.SelectedTaskID = response.SelectedTask.ID
	}
	if response.Run != nil {
		attempt.RunID = response.Run.ID
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.store.RecordDispatchAttempt(recordCtx, attempt); err != nil {
		log.Printf("redline %s dispatch attempt history failed: %v", request.ProviderAccountID, err)
		if dispatchErr == nil && !admitted {
			dispatchErr = err
		}
	}
	if dispatchErr != nil && s.notifier != nil {
		event := domain.NotificationEvent{
			Version: 1, Type: domain.EventSchedulerError, OccurredAt: s.now().UTC(),
			ProviderAccountID: request.ProviderAccountID, Message: "Redline scheduler dispatch failed",
			Data: map[string]string{"trigger": trigger, "error": dispatchErr.Error()},
		}
		if err := s.notifier.Notify(ctx, event); err != nil {
			log.Printf("redline %s scheduler error notification failed: %v", request.ProviderAccountID, err)
		}
	}
	return response, admitted, dispatchErr
}

func (s *Server) dispatchCore(
	ctx context.Context,
	request schedulerRequest,
	trigger string,
) (schedulerResponse, bool, error) {
	response := schedulerResponse{Trigger: trigger}
	paused, err := s.store.ProviderPaused(ctx, request.ProviderAccountID)
	if err != nil {
		return response, false, err
	}
	if paused {
		response.Result = decision.Result{Decision: decision.Wait, Mode: decision.ModePaused, Reason: "provider is paused"}
		return response, false, s.recordSchedulerResponse(ctx, request.ProviderAccountID, response)
	}
	configuredProvider, configured := s.config.Providers[request.ProviderAccountID]
	if !configured {
		return response, false, fmt.Errorf("provider %q is not configured", request.ProviderAccountID)
	}
	active, err := s.store.ActiveRunCount(ctx, request.ProviderAccountID)
	if err != nil {
		return response, false, err
	}
	maxConcurrent := configuredProvider.EffectiveMaxConcurrentRuns()
	if active >= maxConcurrent {
		response.Result = decision.Result{Decision: decision.Wait, Mode: decision.ModeActive,
			Reason: fmt.Sprintf("provider concurrency limit %d reached", maxConcurrent)}
		return response, false, s.recordSchedulerResponse(ctx, request.ProviderAccountID, response)
	}
	response.Snapshot, response.Result, err = s.evaluateProvider(ctx, request.ProviderAccountID)
	if err != nil {
		return response, false, err
	}
	task, profile, revision, selectedResult, err := s.selectTask(
		ctx, request.ProviderAccountID, request.CurrentRevision, response.Snapshot, response.Result,
	)
	response.Result = selectedResult
	if errors.Is(err, store.ErrNotFound) {
		return response, false, s.recordSchedulerResponse(ctx, request.ProviderAccountID, response)
	}
	if err != nil {
		return response, false, err
	}
	run, err := s.store.AdmitTaskWithLimits(
		ctx, uuid.NewString(), task.ID, request.ProviderAccountID, revision, response.Result.RequiredPools,
		store.AdmissionLimits{Provider: maxConcurrent, Pools: configuredProvider.PoolConcurrency}, s.now(),
	)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			response.Result.Decision = decision.Wait
			response.Result.Mode = decision.ModeActive
			response.Result.Reason = err.Error()
			response.Result.TaskSelectionReason = "another scheduler request consumed concurrency first"
			return response, false, s.recordSchedulerResponse(ctx, request.ProviderAccountID, response)
		}
		return response, false, err
	}
	response.SelectedTask = &task
	response.Run = &run
	if err := s.recordSchedulerResponse(ctx, request.ProviderAccountID, response); err != nil {
		log.Printf("redline run %s decision history failed: %v", run.ID, err)
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		if err := s.executor.Execute(context.Background(), run, task, profile); err != nil {
			log.Printf("redline run %s execution bookkeeping failed: %v", run.ID, err)
		}
	}()
	return response, true, nil
}

func (s *Server) selectTask(
	ctx context.Context, provider, suppliedRevision string,
	snapshot decision.UsageSnapshot, base decision.Result,
) (domain.Task, domain.ExecutionProfile, string, decision.Result, error) {
	now := s.now()
	tasks, err := s.store.DispatchCandidates(ctx, provider)
	if err != nil {
		return domain.Task{}, domain.ExecutionProfile{}, "", base, err
	}
	rejections := make([]decision.CandidateRejection, 0)
	for _, task := range tasks {
		if task.LastCompletedAt != nil && task.MinInterval > 0 {
			eligibleAt := task.LastCompletedAt.Add(task.MinInterval)
			if now.Before(eligibleAt) {
				rejections = appendCandidateRejection(rejections, task.ID,
					"cooldown until "+eligibleAt.UTC().Format(time.RFC3339))
				continue
			}
		}
		profile, err := s.store.GetProfile(ctx, task.ExecutionProfileID)
		if err != nil {
			return domain.Task{}, domain.ExecutionProfile{}, "", base, err
		}
		revision := suppliedRevision
		if revision == "" && profile.Repository != "" {
			resolved, resolveErr := s.revision.Resolve(ctx, profile)
			if resolveErr == nil {
				revision = resolved
			} else if task.RequireRepoChange {
				rejections = appendCandidateRejection(rejections, task.ID,
					"repository revision could not be read: "+resolveErr.Error())
				continue
			}
		}
		if task.RequireRepoChange {
			if revision == "" {
				rejections = appendCandidateRejection(rejections, task.ID, "repository revision is unavailable")
				continue
			}
			if revision == task.LastSuccessfulSourceRevision {
				rejections = appendCandidateRejection(rejections, task.ID,
					"repository has not changed since the last successful run")
				continue
			}
		}
		result, eligible, reason := s.evaluateCandidateBudget(provider, snapshot, base, profile)
		if !eligible {
			if reason != "" && len(rejections) < 20 {
				rejections = append(rejections, decision.CandidateRejection{TaskID: task.ID, Reason: reason})
			}
			continue
		}
		if reason, err := s.concurrencyRejection(ctx, provider, result.RequiredPools); err != nil {
			return domain.Task{}, domain.ExecutionProfile{}, "", base, err
		} else if reason != "" {
			rejections = appendCandidateRejection(rejections, task.ID, reason)
			continue
		}
		if !dispatchTierEligible(task.DispatchTier, result.UnlockedTier) {
			if len(rejections) < 20 {
				rejections = append(rejections, decision.CandidateRejection{TaskID: task.ID,
					Reason: fmt.Sprintf("requires %s pressure; %s is currently unlocked", task.DispatchTier, result.UnlockedTier)})
			}
			continue
		}
		result.CandidateRejections = rejections
		result.TaskSelectionReason = "selected highest-priority eligible task"
		return task, profile, revision, result, nil
	}
	base.CandidateRejections = rejections
	base.TaskSelectionReason = "no queued tasks are eligible"
	return domain.Task{}, domain.ExecutionProfile{}, "", base,
		fmt.Errorf("%w: no eligible task for provider %q", store.ErrNotFound, provider)
}

func (s *Server) concurrencyRejection(ctx context.Context, providerID string, pools []string) (string, error) {
	configured := s.config.Providers[providerID]
	for _, pool := range pools {
		limit, limited := configured.PoolConcurrency[pool]
		if !limited {
			continue
		}
		active, err := s.store.ActivePoolClaimCount(ctx, providerID, pool)
		if err != nil {
			return "", err
		}
		if active >= limit {
			return fmt.Sprintf("allowance pool %q concurrency limit %d reached", pool, limit), nil
		}
	}
	return "", nil
}

func appendCandidateRejection(
	rejections []decision.CandidateRejection,
	taskID, reason string,
) []decision.CandidateRejection {
	if reason == "" || len(rejections) >= 20 {
		return rejections
	}
	return append(rejections, decision.CandidateRejection{TaskID: taskID, Reason: reason})
}

func (s *Server) evaluateCandidateBudget(
	providerID string,
	snapshot decision.UsageSnapshot,
	base decision.Result,
	profile domain.ExecutionProfile,
) (decision.Result, bool, string) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return base, false, "provider is not configured"
	}
	group, routing, err := configured.ResolveModelGroup(profile.Model, profile.BudgetModelGroup)
	if err != nil {
		return base, false, err.Error()
	}
	required := []string{"weekly"}
	if snapshot.Short != nil {
		required = append([]string{"session"}, required...)
	}
	poolResults := []decision.PoolResult{{
		Pool: "weekly", Decision: base.Decision, Mode: base.Mode, Reason: base.Reason,
		Remaining: snapshot.Weekly.Remaining, UnlockedTier: base.UnlockedTier,
	}}
	triggering := make([]string, 0, 2)
	if base.Decision == decision.Run {
		triggering = append(triggering, "weekly")
	}
	if base.Decision == decision.Unknown {
		return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
			"shared allowance decision is unknown: " + base.Reason
	}
	policy := s.config.Policies[base.Policy]
	if snapshot.Short != nil && snapshot.Short.Remaining <= policy.RollingReserve {
		return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
			"shared session reserve is protected"
	}
	if snapshot.Weekly.Remaining <= 0 {
		return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
			"shared weekly allowance is exhausted"
	}
	if group != "" {
		poolKey := "model:" + group + ":weekly"
		required = append(required, poolKey)
		allowance, found := snapshot.Allowance(poolKey)
		if !found {
			return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
				poolKey + " allowance is missing"
		}
		if allowance.Remaining <= 0 {
			return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
				poolKey + " allowance is exhausted"
		}
		if !allowance.ResetsAt.After(s.now()) {
			return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
				poolKey + " reset is not in the future"
		}
		thresholds, thresholdErr := policy.DecisionThresholds()
		maxAge, ageErr := s.config.SnapshotAge()
		if thresholdErr != nil || ageErr != nil {
			return base, false, "model-specific pace policy is invalid"
		}
		poolSnapshot := decision.UsageSnapshot{
			Provider: snapshot.Provider, ObservedAt: snapshot.ObservedAt,
			Weekly: decision.UsageWindow{Remaining: allowance.Remaining, ResetsAt: allowance.ResetsAt},
			Source: snapshot.Source, Confidence: snapshot.Confidence,
		}
		poolDecision := decision.Evaluate(decision.Input{
			Snapshot: poolSnapshot, WindowWeeklyCost: configured.WindowWeeklyCost,
			TriggerMargin: policy.TriggerMargin, RollingReserve: policy.RollingReserve,
			PaceThresholds: thresholds, Now: s.now(), MaxSnapshotAge: maxAge,
		})
		poolResults = append(poolResults, decision.PoolResult{
			Pool: poolKey, Decision: poolDecision.Decision, Mode: poolDecision.Mode,
			Reason: poolDecision.Reason, Remaining: allowance.Remaining, UnlockedTier: poolDecision.UnlockedTier,
		})
		if poolDecision.Decision == decision.Unknown {
			return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false,
				poolKey + " decision is unknown: " + poolDecision.Reason
		}
		if poolDecision.Decision == decision.Run {
			triggering = append(triggering, poolKey)
		}
	}
	if len(triggering) == 0 {
		return decorateBudgetResult(base, profile.Model, routing, required, triggering, poolResults), false, ""
	}
	result := base
	if base.Decision != decision.Run {
		result.Decision = decision.Run
		result.Mode = decision.ModePace
		result.Reason = "model-specific allowance meets pace threshold"
	}
	for _, pool := range poolResults {
		if dispatchTierRank(pool.UnlockedTier) > dispatchTierRank(result.UnlockedTier) {
			result.UnlockedTier = pool.UnlockedTier
		}
	}
	return decorateBudgetResult(result, profile.Model, routing, required, triggering, poolResults), true, ""
}

func dispatchTierEligible(required, unlocked domain.DispatchTier) bool {
	if required == "" {
		required = domain.DispatchBehind
	}
	return dispatchTierRank(unlocked) >= dispatchTierRank(required)
}

func dispatchTierRank(tier domain.DispatchTier) int {
	switch tier {
	case domain.DispatchExpiring:
		return 3
	case domain.DispatchWellBehind:
		return 2
	case domain.DispatchBehind:
		return 1
	default:
		return 0
	}
}

func decorateBudgetResult(
	result decision.Result, model, routing string, required, triggering []string, pools []decision.PoolResult,
) decision.Result {
	result.Model = model
	result.ModelRouting = routing
	result.RequiredPools = append([]string(nil), required...)
	result.TriggeringPools = append([]string(nil), triggering...)
	result.PoolResults = append([]decision.PoolResult(nil), pools...)
	return result
}

func (s *Server) recordSchedulerResponse(ctx context.Context, provider string, response schedulerResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	record := domain.SchedulerDecision{ProviderAccountID: provider, DecisionJSON: encoded}
	if response.SelectedTask != nil {
		record.SelectedTaskID = response.SelectedTask.ID
	}
	_, err = s.store.RecordSchedulerDecision(ctx, record, s.now())
	return err
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.ListRuns(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listRunEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.ListRunEvents(r.Context(), r.PathValue("run"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		writeError(w, err)
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	var path string
	switch stream {
	case "stdout":
		path = run.OutputFile
	case "stderr":
		path = run.ErrorFile
	case "prepare_stdout":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "prepare", "stdout")
	case "prepare_stderr":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "prepare", "stderr")
	case "finalize_stdout":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "finalize", "stdout")
	case "finalize_stderr":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "finalize", "stderr")
	default:
		writeJSON(w, http.StatusBadRequest, problem{Error: "unsupported log stream"})
		return
	}
	if path == "" {
		writeJSON(w, http.StatusNotFound, problem{Error: stream + " artifact is not available"})
		return
	}
	tailBytes := int64(32 * 1024)
	if configured := r.URL.Query().Get("tail_bytes"); configured != "" {
		tailBytes, err = strconv.ParseInt(configured, 10, 64)
		if err != nil || tailBytes <= 0 {
			writeJSON(w, http.StatusBadRequest, problem{Error: "tail_bytes must be a positive integer"})
			return
		}
	}
	tail, err := s.artifacts.ReadTail(path, tailBytes)
	if errors.Is(err, artifacts.ErrOutsideRoot) {
		writeJSON(w, http.StatusForbidden, problem{Error: err.Error()})
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		if stream != "stdout" && stream != "stderr" {
			writeJSON(w, http.StatusOK, artifacts.Tail{})
			return
		}
		writeJSON(w, http.StatusNotFound, problem{Error: "artifact file does not exist"})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tail)
}

type decisionResponse struct {
	Snapshot decision.UsageSnapshot `json:"snapshot"`
	Result   decision.Result        `json:"result"`
}

func (s *Server) evaluateScheduler(w http.ResponseWriter, r *http.Request) {
	var request schedulerRequest
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if request.ProviderAccountID == "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider_account_id is required"})
		return
	}
	paused, err := s.store.ProviderPaused(r.Context(), request.ProviderAccountID)
	if err != nil {
		writeError(w, err)
		return
	}
	if paused {
		response := schedulerResponse{Result: decision.Result{
			Decision: decision.Wait, Mode: decision.ModePaused, Reason: "provider is paused",
		}}
		encoded, _ := json.Marshal(response)
		_, err := s.store.RecordSchedulerDecision(r.Context(), domain.SchedulerDecision{
			ProviderAccountID: request.ProviderAccountID, DecisionJSON: encoded,
		}, s.now())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	snapshot, result, err := s.evaluateProvider(r.Context(), request.ProviderAccountID)
	if err != nil {
		writeError(w, err)
		return
	}
	response := schedulerResponse{Snapshot: snapshot, Result: result}
	task, _, _, selectedResult, err := s.selectTask(
		r.Context(), request.ProviderAccountID, request.CurrentRevision, snapshot, result,
	)
	response.Result = selectedResult
	if err == nil {
		response.SelectedTask = &task
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, err)
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		writeError(w, err)
		return
	}
	record := domain.SchedulerDecision{
		ProviderAccountID: request.ProviderAccountID,
		DecisionJSON:      encoded,
	}
	if response.SelectedTask != nil {
		record.SelectedTaskID = response.SelectedTask.ID
	}
	if _, err := s.store.RecordSchedulerDecision(r.Context(), record, s.now()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	decisions, err := s.store.ListSchedulerDecisions(r.Context(), provider, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decisions)
}

func (s *Server) listAttempts(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, problem{Error: "provider is required"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	attempts, err := s.store.ListDispatchAttempts(r.Context(), provider, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attempts)
}

func (s *Server) evaluateProvider(
	ctx context.Context,
	providerID string,
) (decision.UsageSnapshot, decision.Result, error) {
	snapshot, _, err := s.fetchAndStore(ctx, providerID)
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	selection, err := s.effectiveProviderPolicy(ctx, providerID)
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	thresholds, err := selection.Definition.DecisionThresholds()
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	maxAge, err := s.config.SnapshotAge()
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	estimate, err := s.calibration(ctx, providerID)
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	result := decision.Evaluate(decision.Input{
		Snapshot: snapshot, WindowWeeklyCost: estimate.EffectiveCost,
		WindowWeeklyCostSource: string(estimate.Source), CalibrationConfidence: string(estimate.Confidence),
		TriggerMargin: selection.Definition.TriggerMargin, RollingReserve: selection.Definition.RollingReserve,
		PaceThresholds: thresholds, Now: s.now(), MaxSnapshotAge: maxAge,
	})
	result.Policy = selection.Policy
	return snapshot, result, nil
}

func (s *Server) calibration(ctx context.Context, providerID string) (calibration.Estimate, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return calibration.Estimate{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	snapshots, err := s.store.ListSnapshots(ctx, configured.Provider, 500)
	if err != nil {
		return calibration.Estimate{}, err
	}
	return calibration.EstimateWindowWeeklyCost(
		configured.Provider, snapshots, configured.WindowWeeklyCost, s.now(),
	), nil
}

func (s *Server) fetchAndStore(
	ctx context.Context,
	providerID string,
) (decision.UsageSnapshot, config.Provider, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return decision.UsageSnapshot{}, config.Provider{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	snapshot, raw, err := s.usageSources.Fetch(ctx, providerID, configured)
	if err != nil {
		return decision.UsageSnapshot{}, config.Provider{}, err
	}
	if err := s.store.SaveSnapshot(ctx, snapshot, raw); err != nil {
		return decision.UsageSnapshot{}, config.Provider{}, err
	}
	return snapshot, configured, nil
}

type problem struct {
	Error string `json:"error"`
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid JSON: request must contain one object")
	}
	return nil
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, store.ErrConflict) {
		status = http.StatusConflict
	} else if strings.Contains(err.Error(), "not configured") {
		status = http.StatusNotFound
	} else if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "must be") {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, problem{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid duration")
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid duration")
	}
	return duration, nil
}
