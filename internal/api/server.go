package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/openusage"
	"github.com/jfox/redline/internal/store"
)

type Server struct {
	config config.Config
	store  *store.DB
	now    func() time.Time
}

func NewServer(cfg config.Config, database *store.DB, now func() time.Time) http.Handler {
	server := &Server{config: cfg, store: database, now: now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", server.health)
	mux.HandleFunc("POST /v1/providers/{provider}/refresh", server.refresh)
	mux.HandleFunc("GET /v1/providers/{provider}/status", server.status)
	mux.HandleFunc("POST /v1/providers/{provider}/decision", server.providerDecision)
	mux.HandleFunc("POST /v1/providers/{provider}/{control}", server.providerControl)
	mux.HandleFunc("GET /v1/profiles", server.listProfiles)
	mux.HandleFunc("POST /v1/profiles", server.createProfile)
	mux.HandleFunc("GET /v1/tasks", server.listTasks)
	mux.HandleFunc("POST /v1/tasks", server.createTask)
	mux.HandleFunc("POST /v1/tasks/{task}/{control}", server.taskControl)
	mux.HandleFunc("POST /v1/scheduler/evaluate", server.evaluateScheduler)
	mux.HandleFunc("GET /v1/scheduler/decisions", server.listDecisions)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	snapshot, _, err := s.fetchAndStore(r, r.PathValue("provider"))
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
	snapshot, _, err := s.store.LatestSnapshot(r.Context(), configured.Provider)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) providerDecision(w http.ResponseWriter, r *http.Request) {
	snapshot, result, err := s.evaluateProvider(r, r.PathValue("provider"))
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

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var profile domain.ExecutionProfile
	if err := decodeJSON(r, &profile); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if profile.ID == "" {
		profile.ID = uuid.NewString()
	}
	profile.CreatedAt = s.now().UTC()
	if err := s.store.CreateProfile(r.Context(), profile, profile.CreatedAt); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, profile)
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
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Prompt             string          `json:"prompt"`
	PromptFile         string          `json:"prompt_file"`
	Priority           int             `json:"priority"`
	ExecutionProfileID string          `json:"execution_profile_id"`
	Type               domain.TaskType `json:"type"`
	MinInterval        string          `json:"min_interval"`
	RequireRepoChange  bool            `json:"require_repo_change"`
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
		Type: request.Type, MinInterval: interval, RequireRepoChange: request.RequireRepoChange,
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
	SelectedTask *domain.Task           `json:"selected_task,omitempty"`
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
	snapshot, result, err := s.evaluateProvider(r, request.ProviderAccountID)
	if err != nil {
		writeError(w, err)
		return
	}
	response := schedulerResponse{Snapshot: snapshot, Result: result}
	if result.Decision == decision.Run {
		task, err := s.store.NextEligibleTask(
			r.Context(), request.ProviderAccountID, s.now(), request.CurrentRevision,
		)
		if err == nil {
			response.SelectedTask = &task
		} else if !errors.Is(err, store.ErrNotFound) {
			writeError(w, err)
			return
		}
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

func (s *Server) evaluateProvider(
	r *http.Request,
	providerID string,
) (decision.UsageSnapshot, decision.Result, error) {
	snapshot, configured, err := s.fetchAndStore(r, providerID)
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	policy := s.config.Policies[s.config.ActivePolicy]
	thresholds, err := policy.DecisionThresholds()
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	maxAge, err := s.config.SnapshotAge()
	if err != nil {
		return decision.UsageSnapshot{}, decision.Result{}, err
	}
	result := decision.Evaluate(decision.Input{
		Snapshot: snapshot, WindowWeeklyCost: configured.WindowWeeklyCost,
		TriggerMargin: policy.TriggerMargin, RollingReserve: policy.RollingReserve,
		PaceThresholds: thresholds, Now: s.now(), MaxSnapshotAge: maxAge,
	})
	return snapshot, result, nil
}

func (s *Server) fetchAndStore(
	r *http.Request,
	providerID string,
) (decision.UsageSnapshot, config.Provider, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return decision.UsageSnapshot{}, config.Provider{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	client := openusage.Client{BaseURL: configured.OpenUsageURL}
	snapshot, raw, err := client.Fetch(r.Context(), configured.Provider)
	if err != nil {
		return decision.UsageSnapshot{}, config.Provider{}, err
	}
	if err := s.store.SaveSnapshot(r.Context(), snapshot, raw); err != nil {
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
