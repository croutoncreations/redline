// Package demo builds deterministic, isolated Redline data for screenshots,
// product demos, and release recordings. It never reads the user's Redline
// configuration or provider credentials.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/decision"
	"github.com/jfox/redline/internal/discovery"
	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/store"
)

type Scenario struct {
	Name        string
	Description string
}

// Catalog is deterministic and deliberately does not inspect the host PATH or
// read provider configuration files.
func Catalog(now time.Time) discovery.Catalog {
	return discovery.Catalog{GeneratedAt: now.UTC(), Harnesses: []discovery.Harness{
		{ID: "codex-cli", Label: "Codex CLI", Installed: true, Version: "demo", Models: map[string][]discovery.Model{"codex": {{ID: "gpt-5.6", Label: "GPT-5.6", Source: "demo"}}}},
		{ID: "claude-code", Label: "Claude Code", Installed: true, Version: "demo", Models: map[string][]discovery.Model{"claude": {{ID: "claude-opus-4.8", Label: "Claude Opus 4.8", Source: "demo"}}}},
		{ID: "command", Label: "Custom command", Installed: true, Version: "demo"},
	}}
}

type Discoverer struct{ Now func() time.Time }

func (d Discoverer) Discover(context.Context) discovery.Catalog { return Catalog(d.Now()) }

// Executor animates a harmless local run. It writes only below the demo state
// root and never launches a command, contacts a provider, or reads a repository.
type Executor struct {
	Store *store.DB
	Root  string
	Now   func() time.Time
	Delay time.Duration
}

func (e Executor) Execute(ctx context.Context, run domain.Run, task domain.Task, profile domain.ExecutionProfile) error {
	workspace := domain.Workspace{Directory: filepath.Join(e.Root, "workspaces", task.ID), Branch: "redline/demo-" + task.ID}
	if err := os.MkdirAll(workspace.Directory, 0o700); err != nil {
		return err
	}
	if err := e.Store.MarkRunRunning(ctx, run.ID, workspace); err != nil {
		return err
	}
	delay := e.Delay
	if delay <= 0 {
		delay = 1200 * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
	}
	outputDir := filepath.Join(e.Root, "runs", run.ID)
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	output := filepath.Join(outputDir, "stdout.log")
	body := "Synthetic demo run\n\nCompleted: " + task.Name + "\nNo provider, repository, or network was accessed.\n"
	if err := os.WriteFile(output, []byte(body), 0o600); err != nil {
		return err
	}
	model := profile.Model
	if strings.TrimSpace(model) == "" {
		model = "demo-model"
	}
	return e.Store.CompleteRun(ctx, run.ID, domain.RunCompletion{
		State: domain.RunCompleted, ExitCode: 0, OutputFile: output,
		Summary: "Demo job completed without invoking an external agent.", Outcome: "completed",
		Artifacts:      []domain.RunArtifact{{Type: "report", Label: "Synthetic demo report", URL: "https://example.com/redline/demo-report"}},
		ActualProvider: providerName(profile.ProviderAccountID), ActualModel: model,
	}, e.Now().UTC())
}

func Scenarios() []Scenario {
	return []Scenario{
		{"overview", "Balanced product overview with queue, completed work, and artifacts"},
		{"running", "A job actively running with recent completed work"},
		{"attention", "A failed job requiring user attention"},
		{"empty", "Configured providers before the first job is added"},
		{"decision-wait", "One bounded task waiting because no actionable capacity surplus is available"},
		{"decision-run", "The same task admitted because capacity surplus exceeds the pace trigger"},
		{"decision-run-near-expiry", "The same task admitted shortly before a weekly reset with capacity still unused"},
		{"decision-unknown", "The same task held because its synthetic usage sample is stale"},
	}
}

type Environment struct {
	Root      string
	Config    config.Config
	Database  *store.DB
	Snapshots map[string]decision.UsageSnapshot
}

func (e *Environment) Close() error {
	if e == nil || e.Database == nil {
		return nil
	}
	return e.Database.Close()
}

func Create(ctx context.Context, scenario, root string, now time.Time) (*Environment, error) {
	return CreateForProvider(ctx, scenario, "claude-main", root, now)
}

func CreateForProvider(ctx context.Context, scenario, provider, root string, now time.Time) (*Environment, error) {
	if !knownScenario(scenario) {
		return nil, fmt.Errorf("unknown demo scenario %q", scenario)
	}
	if isDecisionScenario(scenario) && provider != "claude-main" && provider != "codex-main" {
		return nil, fmt.Errorf("unknown demo provider %q", provider)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create demo state: %w", err)
	}
	now = now.UTC()
	cfg := fixtureConfig(root, scenario, provider)
	database, err := store.Open(cfg.Database)
	if err != nil {
		return nil, err
	}
	snapshots := fixtureSnapshots(now)
	if isDecisionScenario(scenario) {
		snapshots = decisionFixtureSnapshots(scenario, provider, now)
	}
	env := &Environment{Root: root, Config: cfg, Database: database, Snapshots: snapshots}
	if err := seed(ctx, env, scenario, provider, now); err != nil {
		database.Close()
		return nil, err
	}
	return env, nil
}

func isDecisionScenario(name string) bool { return strings.HasPrefix(name, "decision-") }

func knownScenario(name string) bool {
	for _, scenario := range Scenarios() {
		if scenario.Name == name {
			return true
		}
	}
	return false
}

func fixtureConfig(root, scenario, provider string) config.Config {
	pace := 0.08
	label := scenario
	if isDecisionScenario(scenario) {
		pace = 0.30
		label += " · " + strings.TrimSuffix(provider, "-main")
	}
	return config.Config{
		Database: filepath.Join(root, "redline-demo.db"), RunArtifactsDir: filepath.Join(root, "runs"),
		ActivePolicy: "standard", MaxSnapshotAge: "15m", DemoScenario: label,
		Scheduler:    config.Scheduler{Enabled: true, PollInterval: "5m"},
		UsageMonitor: config.UsageMonitor{Enabled: false},
		Providers: map[string]config.Provider{
			"claude-main": {Provider: "claude", UsageSource: "native", WindowWeeklyCost: .10, Policy: "standard"},
			"codex-main":  {Provider: "codex", UsageSource: "native", WindowWeeklyCost: .10, Policy: "standard"},
		},
		Policies: map[string]config.Policy{
			"standard": {TriggerMargin: .02, RollingReserve: .25, PaceGapTrigger: &pace},
		},
	}
}

func decisionFixtureSnapshots(scenario, provider string, now time.Time) map[string]decision.UsageSnapshot {
	weeklyRemaining, weeklyReset := .30, now.Add(4*24*time.Hour)
	shortRemaining, shortReset := .70, now.Add(2*time.Hour)
	switch scenario {
	case "decision-run":
		weeklyRemaining, weeklyReset = .72, now.Add(2*24*time.Hour)
		shortRemaining = .80
	case "decision-run-near-expiry":
		weeklyRemaining, weeklyReset = .40, now.Add(6*time.Hour)
		shortRemaining = .80
	}
	observedAt := now
	if scenario == "decision-unknown" {
		observedAt = now.Add(-20 * time.Minute)
	}
	target := decision.UsageSnapshot{
		Provider: strings.TrimSuffix(provider, "-main"), ObservedAt: observedAt, Source: "demo", Confidence: "synthetic",
		Weekly: decision.UsageWindow{Remaining: weeklyRemaining, ResetsAt: weeklyReset},
	}
	if provider == "claude-main" {
		target.Short = &decision.UsageWindow{Remaining: shortRemaining, ResetsAt: shortReset}
	}
	result := fixtureSnapshots(now)
	result[provider] = target
	return result
}

func fixtureSnapshots(now time.Time) map[string]decision.UsageSnapshot {
	shortReset := now.Add(2*time.Hour + 12*time.Minute)
	weeklyReset := now.Add(3*24*time.Hour + 8*time.Hour)
	return map[string]decision.UsageSnapshot{
		"claude-main": {
			Provider: "claude", ObservedAt: now, Source: "demo", Confidence: "synthetic",
			Short:      &decision.UsageWindow{Remaining: .68, ResetsAt: shortReset},
			Weekly:     decision.UsageWindow{Remaining: .71, ResetsAt: weeklyReset},
			Allowances: []decision.AllowanceWindow{{Key: "model:fable:weekly", SourceLabel: "Fable", Scope: "model", Role: "weekly", Remaining: .64, ResetsAt: weeklyReset, PeriodDurationSeconds: 604800}},
		},
		"codex-main": {
			Provider: "codex", ObservedAt: now, Source: "demo", Confidence: "synthetic",
			Weekly: decision.UsageWindow{Remaining: .43, ResetsAt: now.Add(4*24*time.Hour + 3*time.Hour)},
		},
	}
}

func seed(ctx context.Context, env *Environment, scenario, provider string, now time.Time) error {
	for _, snapshot := range env.Snapshots {
		raw, _ := json.Marshal(snapshot)
		if err := env.Database.SaveSnapshot(ctx, snapshot, raw); err != nil {
			return err
		}
	}
	if isDecisionScenario(scenario) {
		return seedDecisionScenario(ctx, env, scenario, provider, now)
	}
	if err := seedDecisions(ctx, env.Database, now); err != nil {
		return err
	}
	if scenario == "empty" {
		return nil
	}
	profiles := []domain.ExecutionProfile{
		{ID: "codex-demo", ProviderAccountID: "codex-main", HarnessType: "codex-cli", Model: "gpt-5.6", WorkspaceProvider: "git-worktree", Repository: "/Demo/Atlas", BaseBranch: "main", RequireClean: true},
		{ID: "claude-demo", ProviderAccountID: "claude-main", HarnessType: "claude-code", Model: "claude-opus-4.8", WorkspaceProvider: "git-worktree", Repository: "/Demo/Atlas", BaseBranch: "main", RequireClean: true},
	}
	for _, profile := range profiles {
		if err := env.Database.CreateProfile(ctx, profile, now.Add(-14*24*time.Hour)); err != nil {
			return err
		}
	}
	tasks := []domain.Task{
		{ID: "bug-hunt", Name: "Find and fix one real bug", Prompt: "Find, reproduce, and fix one meaningful bug. Open a draft pull request.", Priority: 90, ExecutionProfileID: "codex-demo", Type: domain.Recurring, DispatchTier: domain.DispatchBehind, MinInterval: 48 * time.Hour},
		{ID: "test-coverage", Name: "Improve test coverage", Prompt: "Add a focused regression test for one high-risk path.", Priority: 80, ExecutionProfileID: "claude-demo", Type: domain.Recurring, DispatchTier: domain.DispatchBehind, MinInterval: 72 * time.Hour},
		{ID: "release-notes", Name: "Draft release notes", Prompt: "Summarize the latest user-visible changes in concise release notes.", Priority: 65, ExecutionProfileID: "claude-demo", Type: domain.OneOff, DispatchTier: domain.DispatchWellBehind},
		{ID: "dependency-review", Name: "Review dependency health", Prompt: "Review one outdated dependency and recommend a safe next step.", Priority: 55, ExecutionProfileID: "codex-demo", Type: domain.Recurring, DispatchTier: domain.DispatchExpiring, MinInterval: 7 * 24 * time.Hour},
	}
	for _, task := range tasks {
		if err := env.Database.CreateTask(ctx, task, now.Add(-10*24*time.Hour)); err != nil {
			return err
		}
	}
	if err := completedRun(ctx, env.Database, "demo-run-release", "release-notes", "claude-main", now.Add(-90*time.Minute), now.Add(-82*time.Minute), "Drafted concise release notes for the upcoming version.", nil); err != nil {
		return err
	}
	if err := completedRun(ctx, env.Database, "demo-run-bug", "bug-hunt", "codex-main", now.Add(-26*time.Hour), now.Add(-25*time.Hour-42*time.Minute), "Fixed a race in cache refresh and opened a draft pull request.", []domain.RunArtifact{{Type: "pull_request", Label: "Draft pull request", URL: "https://example.com/pull/142"}}); err != nil {
		return err
	}
	switch scenario {
	case "running":
		run, err := env.Database.AdmitTask(ctx, "demo-run-active", "test-coverage", "claude-main", "demo-revision-2", now.Add(-4*time.Minute))
		if err != nil {
			return err
		}
		return env.Database.MarkRunRunning(ctx, run.ID, domain.Workspace{Directory: "/Demo/Atlas/.worktrees/test-coverage", Branch: "redline/test-coverage"})
	case "attention":
		run, err := env.Database.AdmitTask(ctx, "demo-run-failed", "test-coverage", "claude-main", "demo-revision-3", now.Add(-12*time.Minute))
		if err != nil {
			return err
		}
		if err := env.Database.MarkRunRunning(ctx, run.ID, domain.Workspace{Directory: "/Demo/Atlas/.worktrees/test-coverage"}); err != nil {
			return err
		}
		return env.Database.CompleteRun(ctx, run.ID, domain.RunCompletion{State: domain.RunFailed, ExitCode: 1, Error: "Claude Code is signed out. Reconnect the provider, then retry this job.", Summary: "The agent could not start because its demo credentials need attention.", ActualProvider: "claude", ActualModel: "claude-opus-4.8"}, now.Add(-10*time.Minute))
	}
	return nil
}

func seedDecisionScenario(ctx context.Context, env *Environment, scenario, provider string, now time.Time) error {
	profileID, harness, model := "claude-decision-demo", "claude-code", "claude-opus-4.8"
	if provider == "codex-main" {
		profileID, harness, model = "codex-decision-demo", "codex-cli", "gpt-5.6"
	}
	profile := domain.ExecutionProfile{ID: profileID, ProviderAccountID: provider, HarnessType: harness, Model: model,
		WorkspaceProvider: "git-worktree", Repository: "/Demo/Atlas", BaseBranch: "main", RequireClean: true}
	if err := env.Database.CreateProfile(ctx, profile, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	task := domain.Task{ID: "demo-decision-task", Name: "Find and fix one real bug",
		Prompt:   "Find, reproduce, and fix one meaningful bug. Add a regression test and open a draft pull request.",
		Priority: 90, ExecutionProfileID: profileID, Type: domain.Recurring, DispatchTier: domain.DispatchBehind, MinInterval: 48 * time.Hour}
	if err := env.Database.CreateTask(ctx, task, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	policy := env.Config.Policies["standard"]
	result := decision.Evaluate(decision.Input{
		Snapshot: env.Snapshots[provider], WindowWeeklyCost: env.Config.Providers[provider].WindowWeeklyCost,
		WindowWeeklyCostSource: "demo", CalibrationConfidence: "synthetic",
		TriggerMargin: policy.TriggerMargin, RollingReserve: policy.RollingReserve, PaceGapTrigger: policy.PaceGapTrigger,
		Now: now, MaxSnapshotAge: 15 * time.Minute,
	})
	result.Policy = "standard"
	payload, _ := json.Marshal(map[string]any{"snapshot": env.Snapshots[provider], "result": result})
	selectedTask := ""
	outcome := domain.DispatchWait
	if result.Decision == decision.Admit {
		selectedTask = task.ID
		outcome = domain.DispatchNoTask
	}
	if _, err := env.Database.RecordSchedulerDecision(ctx, domain.SchedulerDecision{
		ProviderAccountID: provider, SelectedTaskID: selectedTask, DecisionJSON: payload,
	}, now.Add(-30*time.Second)); err != nil {
		return err
	}
	_, err := env.Database.RecordDispatchAttempt(ctx, domain.DispatchAttempt{
		ProviderAccountID: provider, Trigger: "demo", Outcome: outcome, Decision: string(result.Decision),
		Mode: string(result.Mode), Reason: demoDecisionReason(scenario, provider, env.Snapshots[provider], result, policy.RollingReserve), SelectedTaskID: selectedTask,
		StartedAt: now.Add(-30 * time.Second), CompletedAt: now.Add(-29 * time.Second),
	})
	return err
}

func demoDecisionReason(scenario, provider string, snapshot decision.UsageSnapshot, result decision.Result, reserve float64) string {
	if result.Decision == decision.Unknown {
		return "Usage sample is stale; waiting for fresh evidence"
	}
	if result.Decision == decision.Wait {
		return "No actionable capacity surplus yet"
	}
	weekly := fmt.Sprintf("%.0f%% weekly remains", snapshot.Weekly.Remaining*100)
	if scenario == "decision-run-near-expiry" {
		if provider == "claude-main" {
			return fmt.Sprintf("Resets in 6 hours; %s; %.0f%% of the current 5-hour window held in reserve; remaining throughput cannot consume the surplus", weekly, reserve*100)
		}
		return fmt.Sprintf("Resets in 6 hours; %s; no current 5-hour limit; %.0f%% capacity surplus", weekly, result.PaceGap*100)
	}
	return fmt.Sprintf("%.0f%% capacity surplus versus time remaining; above the configured trigger", result.PaceGap*100)
}

func completedRun(ctx context.Context, db *store.DB, id, task, provider string, started, completed time.Time, summary string, artifacts []domain.RunArtifact) error {
	run, err := db.AdmitTask(ctx, id, task, provider, "demo-revision-1", started)
	if err != nil {
		return err
	}
	workspace := domain.Workspace{Directory: "/Demo/Atlas/.worktrees/" + task, Branch: "redline/" + task}
	if err := db.MarkRunRunning(ctx, run.ID, workspace); err != nil {
		return err
	}
	return db.CompleteRun(ctx, run.ID, domain.RunCompletion{State: domain.RunCompleted, ExitCode: 0, Summary: summary, Outcome: "completed", Artifacts: artifacts, ActualProvider: providerName(provider), ActualModel: demoModel(provider)}, completed)
}

func providerName(id string) string {
	if id == "claude-main" {
		return "claude"
	}
	return "codex"
}
func demoModel(id string) string {
	if id == "claude-main" {
		return "claude-opus-4.8"
	}
	return "gpt-5.6"
}

func seedDecisions(ctx context.Context, db *store.DB, now time.Time) error {
	items := []struct {
		provider string
		result   decision.Result
		at       time.Time
	}{
		{"claude-main", decision.Result{Decision: decision.Admit, Policy: "standard", Mode: decision.ModePace, Reason: "14% weekly capacity surplus; high-surplus tasks are eligible", PaceGap: .14, UnlockedTier: domain.DispatchWellBehind}, now.Add(-2 * time.Minute)},
		{"codex-main", decision.Result{Decision: decision.Wait, Policy: "standard", Mode: decision.ModePace, Reason: "3% weekly capacity surplus; below the standard dispatch threshold", PaceGap: .03, UnlockedTier: domain.DispatchBehind}, now.Add(-3 * time.Minute)},
	}
	for _, item := range items {
		payload, _ := json.Marshal(map[string]any{"snapshot": fixtureSnapshots(now)[item.provider], "result": item.result})
		if _, err := db.RecordSchedulerDecision(ctx, domain.SchedulerDecision{ProviderAccountID: item.provider, DecisionJSON: payload}, item.at); err != nil {
			return err
		}
		outcome := domain.DispatchWait
		if item.result.Decision == decision.Admit {
			outcome = domain.DispatchNoTask
		}
		if _, err := db.RecordDispatchAttempt(ctx, domain.DispatchAttempt{ProviderAccountID: item.provider, Trigger: "automatic", Outcome: outcome, Decision: string(item.result.Decision), Mode: string(item.result.Mode), Reason: item.result.Reason, StartedAt: item.at, CompletedAt: item.at.Add(120 * time.Millisecond)}); err != nil {
			return err
		}
	}
	return nil
}
