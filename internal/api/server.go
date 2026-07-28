package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/discovery"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/execution"
	"github.com/jfox/redline/internal/harness"
	"github.com/jfox/redline/internal/hermes"
	"github.com/jfox/redline/internal/nativeusage"
	"github.com/jfox/redline/internal/notification"
	"github.com/jfox/redline/internal/openusage"
	autoscheduler "github.com/jfox/redline/internal/scheduler"
	"github.com/jfox/redline/internal/store"
	"github.com/jfox/redline/internal/tokenlog"
	"github.com/jfox/redline/internal/usage"
	"github.com/jfox/redline/internal/workspace"
)

// This file holds core Server plumbing: construction, HTTP entry point,
// authentication, health, and helpers shared across the other server_*.go
// files. Handlers are grouped by concern into server_profiles.go,
// server_tasks.go, server_providers.go, server_dispatch.go, and
// server_runs.go, following the same per-concern split already used by
// dashboard.go and runtime.go in this package.

type Executor interface {
	Execute(context.Context, domain.Run, domain.Task, domain.ExecutionProfile) error
}

type Notifier interface {
	Notify(context.Context, domain.NotificationEvent) error
}

type HarnessDiscoverer interface {
	Discover(context.Context) discovery.Catalog
}

type HermesDiscoverer interface {
	Discover(context.Context, domain.RuntimeConnection) (hermes.Discovery, error)
	ListJobs(context.Context, domain.RuntimeConnection) ([]hermes.Job, error)
	TriggerJob(context.Context, domain.RuntimeConnection, string) (hermes.Job, error)
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
	hermes       HermesDiscoverer
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
		Store: database, Workspaces: workspace.Manager{OutputDirectory: cfg.ArtifactsDirectory()},
		Harness:  &harness.Adapter{Contexts: database},
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
		hermes:    hermes.Client{},
	}
	server.usageSources = usage.NewManager(
		openusage.Source{},
		nativeusage.Client{Credentials: nativeusage.NewDefaultCredentials(), Now: now},
		now,
	)
	if maxSnapshotAge, err := cfg.SnapshotAge(); err == nil {
		server.usageSources.MaxSnapshotAge = maxSnapshotAge
	}
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
	mux.HandleFunc("PATCH /v1/providers/{provider}/concurrency", server.updateProviderConcurrency)
	mux.HandleFunc("POST /v1/providers/{provider}/{control}", server.providerControl)
	mux.HandleFunc("GET /v1/profiles", server.listProfiles)
	mux.HandleFunc("GET /v1/profile-options", server.profileOptions)
	mux.HandleFunc("POST /v1/profiles", server.createProfile)
	mux.HandleFunc("GET /v1/profiles/{profile}", server.getProfile)
	mux.HandleFunc("PATCH /v1/profiles/{profile}", server.updateProfile)
	mux.HandleFunc("DELETE /v1/profiles/{profile}", server.deleteProfile)
	mux.HandleFunc("GET /v1/runtime-connections/imports", server.discoverRuntimeImports)
	mux.HandleFunc("GET /v1/runtime-connections", server.listRuntimeConnections)
	mux.HandleFunc("POST /v1/runtime-connections", server.createRuntimeConnection)
	mux.HandleFunc("GET /v1/runtime-connections/{connection}", server.getRuntimeConnection)
	mux.HandleFunc("PATCH /v1/runtime-connections/{connection}", server.updateRuntimeConnection)
	mux.HandleFunc("DELETE /v1/runtime-connections/{connection}", server.deleteRuntimeConnection)
	mux.HandleFunc("POST /v1/runtime-connections/{connection}/discover", server.discoverRuntimeConnection)
	mux.HandleFunc("GET /v1/runtime-connections/{connection}/jobs", server.listRuntimeJobs)
	mux.HandleFunc("POST /v1/runtime-connections/{connection}/jobs/{job}/run", server.triggerRuntimeJob)
	mux.HandleFunc("GET /v1/agent-contexts", server.listAgentContexts)
	mux.HandleFunc("POST /v1/agent-contexts", server.createAgentContext)
	mux.HandleFunc("GET /v1/agent-contexts/{context}", server.getAgentContext)
	mux.HandleFunc("PATCH /v1/agent-contexts/{context}", server.updateAgentContext)
	mux.HandleFunc("DELETE /v1/agent-contexts/{context}", server.deleteAgentContext)
	mux.HandleFunc("GET /v1/tasks", server.listTasks)
	mux.HandleFunc("GET /v1/task-templates", server.listTaskTemplates)
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if !loopbackHost(r.Host) {
		writeJSON(w, http.StatusForbidden, problem{Error: "Redline only accepts loopback hosts"})
		return
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !sameOrigin(origin, r) {
		writeJSON(w, http.StatusForbidden, problem{Error: "cross-origin Redline API requests are not allowed"})
		return
	}
	if s.config.APIToken != "" {
		if s.bootstrapDashboardSession(w, r) {
			return
		}
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="Redline"`)
			writeJSON(w, http.StatusUnauthorized, problem{Error: "Redline API authentication is required"})
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

const apiSessionCookie = "redline_api_session"

func (s *Server) bootstrapDashboardSession(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != "/dashboard") {
		return false
	}
	token := r.URL.Query().Get("access_token")
	if token == "" {
		return false
	}
	if !secureTokenEqual(token, s.config.APIToken) {
		writeJSON(w, http.StatusUnauthorized, problem{Error: "invalid Redline dashboard token"})
		return true
	}
	http.SetCookie(w, &http.Cookie{
		Name: apiSessionCookie, Value: s.config.APIToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	clean := *r.URL
	query := clean.Query()
	query.Del("access_token")
	clean.RawQuery = query.Encode()
	http.Redirect(w, r, clean.String(), http.StatusSeeOther)
	return true
}

func (s *Server) authorized(r *http.Request) bool {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(authorization, "Bearer ") &&
		secureTokenEqual(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")), s.config.APIToken) {
		return true
	}
	cookie, err := r.Cookie(apiSessionCookie)
	return err == nil && secureTokenEqual(cookie.Value, s.config.APIToken)
}

func secureTokenEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func loopbackHost(hostPort string) bool {
	host := hostPort
	if parsed, _, err := net.SplitHostPort(hostPort); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

func sameOrigin(origin string, r *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

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
	} else if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "must be") ||
		strings.Contains(err.Error(), "unsupported") {
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
