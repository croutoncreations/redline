package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

func (s *Server) schedulerStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.scheduler.Status())
}

func (s *Server) usageMonitorStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.usageMonitor.Status())
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
	concurrency, err := s.effectiveProviderConcurrency(ctx, provider)
	if err != nil {
		return err
	}
	for range concurrency.MaxConcurrentRuns {
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
	// Service or request shutdown can cancel an in-flight usage fetch or database
	// read. Some SQLite paths return only the driver's error text rather than
	// wrapping context.Canceled, so recognize that exact terminal cause too.
	// Cancellation is a lifecycle event, not a failed dispatch.
	if isContextCancellation(dispatchErr) {
		return response, false, dispatchErr
	}
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

func isContextCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	message := err.Error()
	return message == context.Canceled.Error() ||
		strings.HasSuffix(message, ": "+context.Canceled.Error())
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
	concurrency, err := s.effectiveProviderConcurrency(ctx, request.ProviderAccountID)
	if err != nil {
		return response, false, err
	}
	maxConcurrent := concurrency.MaxConcurrentRuns
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
	limits := store.AdmissionLimits{Provider: maxConcurrent, Pools: configuredProvider.PoolConcurrency}
	if profile.AgentContextID != "" {
		agentContext, contextErr := s.store.GetAgentContext(ctx, profile.AgentContextID)
		if contextErr != nil {
			return response, false, contextErr
		}
		connection, connectionErr := s.store.GetRuntimeConnection(ctx, agentContext.RuntimeConnectionID)
		if connectionErr != nil {
			return response, false, connectionErr
		}
		limits.AgentContextID = agentContext.ID
		limits.AgentContext = agentContext.MaxConcurrentRuns
		limits.RuntimeConnectionID = connection.ID
		limits.RuntimeConnection = connection.MaxConcurrentRuns
	}
	run, err := s.store.AdmitTaskWithLimits(
		ctx, uuid.NewString(), task.ID, request.ProviderAccountID, revision, response.Result.RequiredPools,
		limits, s.now(),
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
			PaceGapTrigger: policy.PaceGapTrigger,
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
