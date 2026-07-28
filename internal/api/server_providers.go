package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jfox/redline/internal/calibration"
	"github.com/jfox/redline/internal/capacity"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/tokenlog"
)

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

type decisionResponse struct {
	Snapshot decision.UsageSnapshot `json:"snapshot"`
	Result   decision.Result        `json:"result"`
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

type providerConcurrencySelection struct {
	MaxConcurrentRuns        int    `json:"max_concurrent_runs"`
	DefaultMaxConcurrentRuns int    `json:"default_max_concurrent_runs"`
	Source                   string `json:"source"`
}

func (s *Server) effectiveProviderConcurrency(
	ctx context.Context,
	providerID string,
) (providerConcurrencySelection, error) {
	configured, ok := s.config.Providers[providerID]
	if !ok {
		return providerConcurrencySelection{}, fmt.Errorf("provider %q is not configured", providerID)
	}
	defaultLimit := configured.EffectiveMaxConcurrentRuns()
	override, err := s.store.ProviderMaxConcurrentRuns(ctx, providerID)
	if err != nil {
		return providerConcurrencySelection{}, err
	}
	if override > 0 {
		return providerConcurrencySelection{
			MaxConcurrentRuns: override, DefaultMaxConcurrentRuns: defaultLimit, Source: "override",
		}, nil
	}
	return providerConcurrencySelection{
		MaxConcurrentRuns: defaultLimit, DefaultMaxConcurrentRuns: defaultLimit, Source: "config",
	}, nil
}

func (s *Server) updateProviderConcurrency(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := s.config.Providers[provider]; !ok {
		writeJSON(w, http.StatusNotFound, problem{Error: "provider is not configured"})
		return
	}
	var request struct {
		MaxConcurrentRuns *int `json:"max_concurrent_runs"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: err.Error()})
		return
	}
	if request.MaxConcurrentRuns == nil {
		writeJSON(w, http.StatusBadRequest, problem{Error: "max_concurrent_runs is required"})
		return
	}
	if *request.MaxConcurrentRuns < 0 {
		writeJSON(w, http.StatusBadRequest, problem{Error: "max_concurrent_runs cannot be negative"})
		return
	}
	if err := s.store.SetProviderMaxConcurrentRuns(r.Context(), provider, *request.MaxConcurrentRuns); err != nil {
		writeError(w, err)
		return
	}
	selection, err := s.effectiveProviderConcurrency(r.Context(), provider)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, selection)
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
		PaceGapTrigger: selection.Definition.PaceGapTrigger,
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
