package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/activity"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/harness"
	"github.com/croutoncreations/redline/internal/workspace"
)

type Store interface {
	MarkRunRunning(context.Context, string, domain.Workspace) error
	CompleteRun(context.Context, string, domain.RunCompletion, time.Time) error
	RecordRunEvent(context.Context, domain.RunEvent) (domain.RunEvent, error)
}

type WorkspaceManager interface {
	Prepare(context.Context, string, string, domain.ExecutionProfile) (domain.Workspace, error)
	Finalize(context.Context, workspace.FinalizeRequest) error
	Cleanup(context.Context, workspace.CleanupRequest) error
}

// UsageLimitHookRunner runs a profile's on_usage_limit_command. The
// workspace manager implements it; it is optional so other managers keep
// working without one.
type UsageLimitHookRunner interface {
	RunUsageLimitHook(context.Context, workspace.UsageLimitRequest) error
}

const (
	// usageLimitRetryMargin is added to a reported reset time so the retry
	// does not start on the edge of the window.
	usageLimitRetryMargin = 2 * time.Minute
	// usageLimitRetryFallback holds a retry when the harness did not say
	// when its limit resets. The scheduler's usage check still gates the
	// retry after that.
	usageLimitRetryFallback = time.Hour
)

type Harness interface {
	Run(context.Context, harness.Request) (harness.Result, error)
}

type Notifier interface {
	Notify(context.Context, domain.NotificationEvent) error
}

type UsageRecorder interface {
	RecordRunUsage(context.Context, domain.Run, domain.ExecutionProfile, string, time.Time) (int, error)
}

type Executor struct {
	Store           Store
	Workspaces      WorkspaceManager
	Harness         Harness
	Notifier        Notifier
	UsageRecorder   UsageRecorder
	OutputDirectory string
	Now             func() time.Time
}

func (e Executor) Execute(
	ctx context.Context,
	run domain.Run,
	task domain.Task,
	profile domain.ExecutionProfile,
) error {
	e.recordEvent(ctx, run.ID, domain.RunEventStarted, map[string]any{
		"task": map[string]any{
			"id": task.ID, "name": task.Name, "type": task.Type,
			"priority": task.Priority, "execution_profile_id": task.ExecutionProfileID,
		},
		"profile": auditProfile(profile),
	})
	e.recordEvent(ctx, run.ID, domain.RunEventWorkspacePrepare, map[string]any{
		"provider":          profile.WorkspaceProvider,
		"repository":        profile.Repository,
		"base_branch":       profile.BaseBranch,
		"prepare_artifacts": e.lifecycleArtifacts(run.ID, "prepare"),
	})
	prepared, err := e.Workspaces.Prepare(ctx, run.ID, task.Name, profile)
	if err != nil {
		e.recordEvent(ctx, run.ID, domain.RunEventWorkspaceFailed, map[string]any{
			"error": err.Error(), "workspace": prepared,
		})
		finalizeState := "skipped"
		finalizeError := ""
		if prepared.Directory != "" {
			if recordErr := e.Store.MarkRunRunning(ctx, run.ID, prepared); recordErr != nil {
				finalizeError = "record partially prepared workspace: " + recordErr.Error()
			} else {
				e.recordEvent(ctx, run.ID, domain.RunEventCleanupStarted, map[string]any{
					"policy": profile.CleanupPolicy, "workspace": prepared,
				})
				cleanupErr := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
					Success: false, Profile: profile, Workspace: prepared,
				})
				if cleanupErr != nil {
					finalizeState = "failed"
					finalizeError = cleanupErr.Error()
					e.recordEvent(ctx, run.ID, domain.RunEventCleanupFailed, map[string]any{"error": cleanupErr.Error()})
				} else {
					finalizeState = "cleanup_completed"
					e.recordEvent(ctx, run.ID, domain.RunEventCleanupCompleted, map[string]any{"policy": profile.CleanupPolicy})
				}
			}
		}
		return e.complete(ctx, run, task, domain.RunCompletion{
			State: domain.RunFailed, ExitCode: -1, Error: "prepare workspace: " + err.Error(),
			FinalizeState: finalizeState, FinalizeError: finalizeError,
		})
	}
	if err := e.Store.MarkRunRunning(ctx, run.ID, prepared); err != nil {
		e.recordEvent(ctx, run.ID, domain.RunEventWorkspaceFailed, map[string]any{
			"error": "record prepared workspace: " + err.Error(), "workspace": prepared,
		})
		finalizeState := "cleanup_completed"
		finalizeError := ""
		e.recordEvent(ctx, run.ID, domain.RunEventCleanupStarted, map[string]any{
			"policy": profile.CleanupPolicy, "workspace": prepared,
		})
		if cleanupErr := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
			Success: false, Profile: profile, Workspace: prepared,
		}); cleanupErr != nil {
			finalizeState = "failed"
			finalizeError = cleanupErr.Error()
			e.recordEvent(ctx, run.ID, domain.RunEventCleanupFailed, map[string]any{"error": cleanupErr.Error()})
		} else {
			e.recordEvent(ctx, run.ID, domain.RunEventCleanupCompleted, map[string]any{"policy": profile.CleanupPolicy})
		}
		return e.complete(ctx, run, task, domain.RunCompletion{
			State: domain.RunFailed, ExitCode: -1, Error: "record prepared workspace: " + err.Error(),
			FinalizeState: finalizeState, FinalizeError: finalizeError,
		})
	}
	if e.Notifier != nil {
		startedAt := e.now().UTC()
		event := domain.NotificationEvent{
			Version: 1, Type: domain.EventRunStarted, OccurredAt: startedAt,
			ProviderAccountID: run.ProviderAccountID, TaskID: task.ID, RunID: run.ID,
			Message: "Redline run started",
			Data:    map[string]string{"state": string(domain.RunRunning)},
		}
		if err := e.Notifier.Notify(ctx, event); err != nil {
			log.Printf("redline run %s start notification failed: %v", run.ID, err)
		}
	}
	e.recordEvent(ctx, run.ID, domain.RunEventWorkspacePrepared, map[string]any{
		"workspace": prepared, "prepare_artifacts": e.lifecycleArtifacts(run.ID, "prepare"),
	})

	e.recordEvent(ctx, run.ID, domain.RunEventHarnessStarted, map[string]any{
		"harness_type": profile.HarnessType, "harness_command": profile.HarnessCommand,
		"harness_args": profile.HarnessArgs, "model": profile.Model,
		"working_directory": prepared.Directory,
	})
	harnessResult, harnessErr := e.Harness.Run(ctx, harness.Request{
		RunID: run.ID, OutputDirectory: e.OutputDirectory,
		Task: task, Profile: profile, Workspace: prepared,
		OnExternalStarted: func(external domain.ExternalRun) error {
			if externalStore, ok := e.Store.(interface {
				UpdateRunExternal(context.Context, string, domain.ExternalRun) error
			}); ok {
				if err := externalStore.UpdateRunExternal(ctx, run.ID, external); err != nil {
					return err
				}
			}
			e.recordEvent(ctx, run.ID, domain.RunEventExternalStarted, external)
			return nil
		},
	})
	if e.UsageRecorder != nil && harnessResult.OutputFile != "" {
		inserted, usageErr := e.UsageRecorder.RecordRunUsage(ctx, run, profile, harnessResult.OutputFile, e.now().UTC())
		if usageErr != nil {
			log.Printf("redline run %s usage accounting failed: %v", run.ID, usageErr)
			e.recordEvent(ctx, run.ID, domain.RunEventUsageFailed, map[string]any{"error": usageErr.Error()})
		} else {
			e.recordEvent(ctx, run.ID, domain.RunEventUsageRecorded, map[string]any{"inserted": inserted})
		}
	}
	usageLimit := harnessErr == nil && harnessResult.UsageLimit != nil
	agentSucceeded := harnessErr == nil && harnessResult.ExitCode == 0 && !usageLimit
	state := domain.RunCompleted
	agentError := ""
	handedOff := false
	if usageLimit {
		agentError = harnessResult.Failure
		handedOff = e.handleUsageLimit(ctx, run, task, profile, prepared, harnessResult)
		if handedOff {
			agentSucceeded = true
		} else {
			state = domain.RunFailed
		}
	} else if !agentSucceeded {
		state = domain.RunFailed
		if harnessErr != nil {
			agentError = harnessErr.Error()
		} else if harnessResult.Failure != "" {
			agentError = harnessResult.Failure
		} else {
			agentError = fmt.Sprintf("harness exited with code %d", harnessResult.ExitCode)
		}
		e.recordEvent(ctx, run.ID, domain.RunEventHarnessFailed, map[string]any{
			"exit_code": harnessResult.ExitCode, "error": agentError,
			"stdout": harnessResult.OutputFile, "stderr": harnessResult.ErrorFile,
		})
	} else {
		e.recordEvent(ctx, run.ID, domain.RunEventHarnessCompleted, map[string]any{
			"exit_code": harnessResult.ExitCode,
			"stdout":    harnessResult.OutputFile, "stderr": harnessResult.ErrorFile,
			"metadata": harnessResult.Metadata,
		})
	}

	status := "completed"
	if !agentSucceeded {
		status = "failed"
	}
	outcome := status
	if usageLimit {
		outcome = "usage_limited"
		if handedOff {
			outcome = "handed_off"
		}
	}
	finalizeState := "completed"
	var lifecycleErrors []string
	e.recordEvent(ctx, run.ID, domain.RunEventFinalizeStarted, map[string]any{
		"configured": profile.FinalizeCommand != "", "artifacts": e.lifecycleArtifacts(run.ID, "finalize"),
	})
	if err := e.Workspaces.Finalize(ctx, workspace.FinalizeRequest{
		RunID: run.ID, TaskID: task.ID, Status: status, Outcome: outcome, ExitCode: harnessResult.ExitCode,
		OutputFile: harnessResult.OutputFile,
		ResultFile: harness.ResultFilePath(e.OutputDirectory, run.ID),
		Profile:    profile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
		e.recordEvent(ctx, run.ID, domain.RunEventFinalizeFailed, map[string]any{
			"error": err.Error(), "artifacts": e.lifecycleArtifacts(run.ID, "finalize"),
		})
	} else {
		e.recordEvent(ctx, run.ID, domain.RunEventFinalizeCompleted, map[string]any{
			"configured": profile.FinalizeCommand != "", "artifacts": e.lifecycleArtifacts(run.ID, "finalize"),
		})
	}
	cleanupProfile := profile
	if usageLimit && !handedOff {
		// The job stopped part way through with nothing to pick it up, so
		// its workspace and session are kept for a resume whatever the
		// cleanup policy says.
		cleanupProfile.CleanupPolicy = "never"
	}
	e.recordEvent(ctx, run.ID, domain.RunEventCleanupStarted, map[string]any{
		"policy": cleanupProfile.CleanupPolicy, "workspace": prepared,
	})
	if err := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
		Success: agentSucceeded, Profile: cleanupProfile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
		e.recordEvent(ctx, run.ID, domain.RunEventCleanupFailed, map[string]any{"error": err.Error()})
	} else {
		e.recordEvent(ctx, run.ID, domain.RunEventCleanupCompleted, map[string]any{"policy": cleanupProfile.CleanupPolicy})
	}

	activityResult := activity.Build(activity.Input{
		State: state, Error: agentError, OutputFile: harnessResult.OutputFile,
		ResultFile: harness.ResultFilePath(e.OutputDirectory, run.ID),
		Workspace:  prepared, Metadata: harnessResult.Metadata,
		Provider: profile.HarnessType, Model: profile.Model,
	})
	var retryAt *time.Time
	if usageLimit {
		if handedOff {
			activityResult.Outcome = "handed_off"
			activityResult.Summary = "Usage limit reached; on_usage_limit_command took over the job."
			activityResult.Warnings = append(activityResult.Warnings, agentError)
			// The run itself did not fail, so its error is cleared.
			agentError = ""
		} else {
			activityResult.Outcome = "usage_limited"
			activityResult.Summary = agentError
			retryAt = e.usageLimitRetry(ctx, run, task, profile, harnessResult.UsageLimit)
			if retryAt != nil {
				activityResult.Summary += " Redline will retry it after " + retryAt.Format(time.RFC3339) + "."
			}
		}
	}
	return e.complete(ctx, run, task, domain.RunCompletion{
		State: state, ExitCode: harnessResult.ExitCode,
		OutputFile: harnessResult.OutputFile, ErrorFile: harnessResult.ErrorFile,
		Error: agentError, FinalizeState: finalizeState,
		FinalizeError: strings.Join(lifecycleErrors, "; "),
		Summary:       activityResult.Summary, Outcome: activityResult.Outcome,
		Artifacts: activityResult.Artifacts, Warnings: activityResult.Warnings,
		ActualProvider: activityResult.ActualProvider, ActualModel: activityResult.ActualModel,
		RetryAt: retryAt,
	})
}

// usageLimitRetry decides whether a usage-limited run's task goes back in
// the queue, and until when it is held.
func (e Executor) usageLimitRetry(
	ctx context.Context,
	run domain.Run,
	task domain.Task,
	profile domain.ExecutionProfile,
	limit *harness.UsageLimit,
) *time.Time {
	if !profile.RetryAfterUsageLimit {
		return nil
	}
	if task.UsageLimitRetries >= domain.MaxUsageLimitRetries {
		e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitRetry, map[string]any{
			"scheduled": false, "retries": task.UsageLimitRetries,
			"reason": fmt.Sprintf("stopped at the usage limit %d times in a row", task.UsageLimitRetries),
		})
		return nil
	}
	now := e.now().UTC()
	retryAt := now.Add(usageLimitRetryFallback)
	if limit != nil && limit.ResetsAt != nil {
		// Compared after the margin: a reset that passed while the hook or
		// finalize ran still holds the retry until reset plus margin.
		if deadline := limit.ResetsAt.UTC().Add(usageLimitRetryMargin); deadline.After(now) {
			retryAt = deadline
		} else {
			// The reported window is already over; retry on the next pass.
			retryAt = now
		}
	}
	e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitRetry, map[string]any{
		"scheduled": true, "retry_at": retryAt.Format(time.RFC3339),
		"retry": task.UsageLimitRetries + 1, "max_retries": domain.MaxUsageLimitRetries,
	})
	return &retryAt
}

// handleUsageLimit records a usage-limit stop and runs the profile's
// on_usage_limit_command. It reports whether the command took over.
func (e Executor) handleUsageLimit(
	ctx context.Context,
	run domain.Run,
	task domain.Task,
	profile domain.ExecutionProfile,
	prepared domain.Workspace,
	result harness.Result,
) bool {
	limit := *result.UsageLimit
	resetsAt := ""
	if limit.ResetsAt != nil {
		resetsAt = limit.ResetsAt.UTC().Format(time.RFC3339)
	}
	e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitReached, map[string]any{
		"exit_code": result.ExitCode, "error": result.Failure,
		"limit_type": limit.LimitType, "message": limit.Message, "resets_at": resetsAt,
		"harness_session_id": limit.SessionID, "workspace": prepared,
		"configured": profile.OnUsageLimitCommand != "",
		"stdout":     result.OutputFile, "stderr": result.ErrorFile,
	})
	if profile.OnUsageLimitCommand == "" {
		return false
	}
	hooks, ok := e.Workspaces.(UsageLimitHookRunner)
	if !ok {
		e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitFailed, map[string]any{
			"error": "this workspace manager cannot run on_usage_limit_command",
		})
		return false
	}
	prompt := limit.Prompt
	if prompt == "" {
		// The prompt the harness ran with; read again only when the
		// harness result did not carry it.
		loaded, err := harness.LoadPrompt(task, prepared.Directory)
		if err != nil || strings.TrimSpace(loaded) == "" {
			reason := "the task prompt is unavailable, so the job was not handed off"
			if err != nil {
				reason += ": " + err.Error()
			}
			e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitFailed, map[string]any{"error": reason})
			return false
		}
		prompt = loaded
	}
	artifacts := e.lifecycleArtifacts(run.ID, "usage_limit")
	e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitHook, map[string]any{"artifacts": artifacts})
	if err := hooks.RunUsageLimitHook(ctx, workspace.UsageLimitRequest{
		RunID: run.ID, TaskID: task.ID, TaskName: task.Name,
		ProviderAccountID: run.ProviderAccountID, Prompt: prompt,
		OutputFile: result.OutputFile, ErrorFile: result.ErrorFile,
		ResultFile:       harness.ResultFilePath(e.OutputDirectory, run.ID),
		HarnessSessionID: limit.SessionID, LimitType: limit.LimitType,
		LimitMessage: limit.Message, ResetsAt: resetsAt,
		Profile: profile, Workspace: prepared,
	}); err != nil {
		e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitFailed, map[string]any{
			"error": err.Error(), "artifacts": artifacts,
		})
		return false
	}
	e.recordEvent(ctx, run.ID, domain.RunEventUsageLimitHanded, map[string]any{"artifacts": artifacts})
	return true
}

func (e Executor) complete(
	ctx context.Context,
	run domain.Run,
	task domain.Task,
	completion domain.RunCompletion,
) error {
	completedAt := e.now().UTC()
	if err := e.Store.CompleteRun(ctx, run.ID, completion, completedAt); err != nil {
		return err
	}
	if completion.State == domain.RunFailed && harness.IsAuthenticationFailure(completion.Error) {
		if controls, ok := e.Store.(interface {
			SetProviderPaused(context.Context, string, bool) error
		}); ok {
			if err := controls.SetProviderPaused(ctx, run.ProviderAccountID, true); err != nil {
				log.Printf("redline run %s pause provider after authentication failure: %v", run.ID, err)
			} else {
				e.recordEvent(ctx, run.ID, domain.RunEventProviderPaused, map[string]any{
					"provider_account_id": run.ProviderAccountID,
					"reason":              "harness authentication failed",
				})
			}
		}
	}
	terminalEvent := domain.RunEventCompleted
	if completion.State == domain.RunFailed {
		terminalEvent = domain.RunEventFailed
	}
	e.recordEvent(ctx, run.ID, terminalEvent, map[string]any{
		"state": completion.State, "exit_code": completion.ExitCode,
		"error": completion.Error, "finalize_state": completion.FinalizeState,
		"finalize_error": completion.FinalizeError,
	})
	if e.Notifier == nil {
		return nil
	}
	eventType := domain.EventRunCompleted
	message := "Redline run completed"
	if completion.State == domain.RunFailed {
		eventType = domain.EventRunFailed
		message = "Redline run failed"
	}
	event := domain.NotificationEvent{
		Version: 1, Type: eventType, OccurredAt: completedAt,
		ProviderAccountID: run.ProviderAccountID, TaskID: task.ID, RunID: run.ID,
		Message: message,
		Data: map[string]string{
			"state": string(completion.State), "exit_code": strconv.Itoa(completion.ExitCode),
			"finalize_state": completion.FinalizeState, "error": completion.Error,
			"outcome": completion.Outcome,
		},
	}
	if completion.RetryAt != nil {
		event.Data["retry_at"] = completion.RetryAt.UTC().Format(time.RFC3339)
	}
	if err := e.Notifier.Notify(ctx, event); err != nil {
		log.Printf("redline run %s notification failed: %v", run.ID, err)
	}
	return nil
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Executor) recordEvent(ctx context.Context, runID, eventType string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Printf("redline run %s encode %s event: %v", runID, eventType, err)
		return
	}
	if _, err := e.Store.RecordRunEvent(ctx, domain.RunEvent{
		RunID: runID, Type: eventType, OccurredAt: e.now().UTC(), Payload: encoded,
	}); err != nil {
		log.Printf("redline run %s record %s event: %v", runID, eventType, err)
	}
}

func (e Executor) lifecycleArtifacts(runID, phase string) map[string]string {
	if e.OutputDirectory == "" {
		return nil
	}
	return map[string]string{
		"stdout": workspace.ArtifactPath(e.OutputDirectory, runID, phase, "stdout"),
		"stderr": workspace.ArtifactPath(e.OutputDirectory, runID, phase, "stderr"),
	}
}

func auditProfile(profile domain.ExecutionProfile) map[string]any {
	return map[string]any{
		"id": profile.ID, "provider_account_id": profile.ProviderAccountID,
		"agent_context_id": profile.AgentContextID,
		"harness_type":     profile.HarnessType, "model": profile.Model,
		"harness_command": profile.HarnessCommand, "harness_args": profile.HarnessArgs,
		"workspace_provider": profile.WorkspaceProvider, "workspace_args": profile.WorkspaceArgs,
		"repository": profile.Repository, "base_branch": profile.BaseBranch,
		"require_clean": profile.RequireClean, "cleanup_policy": profile.CleanupPolicy,
		"prepare_command": profile.PrepareCommand, "finalize_command": profile.FinalizeCommand,
		"on_usage_limit_command": profile.OnUsageLimitCommand, "retry_after_usage_limit": profile.RetryAfterUsageLimit,
	}
}
