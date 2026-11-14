package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	kept := e.takeKeptWorkspace(ctx, run, task, profile)
	e.recordEvent(ctx, run.ID, domain.RunEventWorkspacePrepare, map[string]any{
		"provider":          profile.WorkspaceProvider,
		"repository":        profile.Repository,
		"base_branch":       profile.BaseBranch,
		"prepare_artifacts": e.lifecycleArtifacts(run.ID, "prepare"),
		"resumes_run":       kept.ID,
	})
	var prepared domain.Workspace
	var err error
	if kept.ID != "" {
		// The earlier run's workspace holds its partial work; continue there.
		prepared = kept.Workspace
	} else {
		prepared, err = e.Workspaces.Prepare(ctx, run.ID, task.Name, profile)
	}
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
		ResumeSessionID: kept.External.SessionID,
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
	agentSucceeded := harnessErr == nil && harnessResult.ExitCode == 0
	state := domain.RunCompleted
	agentError := ""
	resume := e.resumePlan(task, profile, harnessResult, agentSucceeded)
	if !agentSucceeded {
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

	if resume != nil {
		e.recordEvent(ctx, run.ID, domain.RunEventResumeScheduled, map[string]any{
			"resume_after": resume.After, "keep_workspace": resume.KeepWorkspace,
			"session": harnessResult.SessionID, "usage_limit": harnessResult.UsageLimit,
			"resume_rejected": harnessResult.ResumeRejected,
		})
	}

	finalizeState := domain.FinalizeDeferred
	var lifecycleErrors []string
	if resume == nil || !resume.KeepWorkspace {
		// A kept workspace is finalized and cleaned up by the run that
		// finishes the task, so partial work is not shipped or removed.
		status := "completed"
		if !agentSucceeded {
			status = "failed"
		}
		finalizeState, lifecycleErrors = e.finalizeAndClean(ctx, run.ID, task.ID, status, harnessResult, profile, prepared, agentSucceeded)
	}

	activityResult := activity.Build(activity.Input{
		State: state, Error: agentError, OutputFile: harnessResult.OutputFile,
		ResultFile: harness.ResultFilePath(e.OutputDirectory, run.ID),
		Workspace:  prepared, Metadata: harnessResult.Metadata,
		Provider: profile.HarnessType, Model: profile.Model,
	})
	completion := domain.RunCompletion{
		State: state, ExitCode: harnessResult.ExitCode,
		OutputFile: harnessResult.OutputFile, ErrorFile: harnessResult.ErrorFile,
		Error: agentError, FinalizeState: finalizeState,
		FinalizeError: strings.Join(lifecycleErrors, "; "),
		Summary:       activityResult.Summary, Outcome: activityResult.Outcome,
		Artifacts: activityResult.Artifacts, Warnings: activityResult.Warnings,
		ActualProvider: activityResult.ActualProvider, ActualModel: activityResult.ActualModel,
		ExternalSessionID: harnessResult.SessionID,
	}
	if resume != nil {
		how := "start it again"
		if resume.KeepWorkspace {
			how = "continue this session in the same workspace"
		}
		completion.Outcome = domain.RunOutcomeUsageLimited
		completion.Summary = fmt.Sprintf("Stopped at the account's usage limit. Redline will %s after %s.",
			how, resume.After.UTC().Format(time.RFC3339))
		if harnessResult.ResumeRejected {
			completion.Summary = "Claude Code could not reopen the earlier session. Redline will start the task again."
		}
		completion.Resume = resume
	}
	return e.complete(ctx, run, task, completion)
}

// finalizeAndClean runs a workspace's finalize hook and cleanup policy and
// returns the resulting finalize state and any errors.
func (e Executor) finalizeAndClean(
	ctx context.Context, runID, taskID, status string, result harness.Result,
	profile domain.ExecutionProfile, prepared domain.Workspace, success bool,
) (string, []string) {
	finalizeState := "completed"
	var lifecycleErrors []string
	e.recordEvent(ctx, runID, domain.RunEventFinalizeStarted, map[string]any{
		"configured": profile.FinalizeCommand != "", "artifacts": e.lifecycleArtifacts(runID, "finalize"),
		"workspace": prepared.Directory,
	})
	if err := e.Workspaces.Finalize(ctx, workspace.FinalizeRequest{
		RunID: runID, TaskID: taskID, Status: status, ExitCode: result.ExitCode,
		OutputFile: result.OutputFile,
		ResultFile: harness.ResultFilePath(e.OutputDirectory, runID),
		Profile:    profile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
		e.recordEvent(ctx, runID, domain.RunEventFinalizeFailed, map[string]any{
			"error": err.Error(), "artifacts": e.lifecycleArtifacts(runID, "finalize"),
		})
	} else {
		e.recordEvent(ctx, runID, domain.RunEventFinalizeCompleted, map[string]any{
			"configured": profile.FinalizeCommand != "", "artifacts": e.lifecycleArtifacts(runID, "finalize"),
		})
	}
	e.recordEvent(ctx, runID, domain.RunEventCleanupStarted, map[string]any{
		"policy": profile.CleanupPolicy, "workspace": prepared,
	})
	if err := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
		Success: success, Profile: profile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
		e.recordEvent(ctx, runID, domain.RunEventCleanupFailed, map[string]any{"error": err.Error()})
	} else {
		e.recordEvent(ctx, runID, domain.RunEventCleanupCompleted, map[string]any{"policy": profile.CleanupPolicy})
	}
	return finalizeState, lifecycleErrors
}

// MaxUsageLimitResumes bounds how many consecutive runs of one task may stop
// at a usage limit and be queued again before the task fails instead.
const MaxUsageLimitResumes = 3

// usageLimitResumeFallback is how long to wait when the harness did not say
// when the limit resets. The scheduler's own usage checks still apply.
const usageLimitResumeFallback = 30 * time.Minute

// usageLimitResumeMargin keeps the next run clear of the reset boundary.
const usageLimitResumeMargin = time.Minute

type runReader interface {
	GetRun(context.Context, string) (domain.Run, error)
}

// takeKeptWorkspace returns the earlier usage-limited run whose session and
// workspace this run continues, or the zero Run to start afresh. When the
// kept workspace cannot be continued (the task changed, the profile cannot
// resume, the session is missing), it is finalized and cleaned up here so it
// is not left behind.
func (e Executor) takeKeptWorkspace(
	ctx context.Context, run domain.Run, task domain.Task, profile domain.ExecutionProfile,
) domain.Run {
	if task.ResumeRunID == "" {
		return domain.Run{}
	}
	runs, ok := e.Store.(runReader)
	if !ok {
		return domain.Run{}
	}
	kept, err := runs.GetRun(ctx, task.ResumeRunID)
	if err != nil {
		e.recordEvent(ctx, run.ID, domain.RunEventResumeFallback, map[string]any{
			"resumes_run": task.ResumeRunID, "reason": "earlier run unavailable: " + err.Error(),
		})
		return domain.Run{}
	}
	exists := false
	if info, statErr := os.Stat(kept.Workspace.Directory); kept.Workspace.Directory != "" && statErr == nil && info.IsDir() {
		exists = true
	}
	reason := ""
	switch {
	case task.ResumeFresh:
		reason = "task prompt or profile changed while waiting"
	case !harness.ProfileCanResume(profile) || !keepsWorkspace(profile):
		reason = "profile cannot resume sessions"
	case kept.External.SessionID == "":
		reason = "earlier run recorded no agent session"
	case !exists:
		reason = "earlier workspace no longer exists"
	case !withinRepository(kept.Workspace.Directory, profile.Repository):
		reason = "earlier workspace is outside the profile's repository"
	default:
		return kept
	}
	e.recordEvent(ctx, run.ID, domain.RunEventResumeFallback, map[string]any{
		"resumes_run": kept.ID, "reason": reason, "workspace": kept.Workspace,
	})
	if exists {
		e.finalizeAndClean(ctx, kept.ID, task.ID, "failed", harness.Result{ExitCode: valueOr(kept.ExitCode, 1), OutputFile: kept.OutputFile},
			profile, kept.Workspace, false)
	}
	return domain.Run{}
}

// keepsWorkspace reports whether the profile gives each run its own
// workspace, so one can be kept for a later run without other tasks running
// in it meanwhile.
func keepsWorkspace(profile domain.ExecutionProfile) bool {
	return profile.WorkspaceProvider == "git-worktree" || profile.WorkspaceProvider == "devx"
}

func withinRepository(directory, repository string) bool {
	relative, err := filepath.Rel(repository, directory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

// resumePlan decides whether a failed run stopped only because the account
// reached its usage limit, and if so when and how the task continues.
func (e Executor) resumePlan(task domain.Task, profile domain.ExecutionProfile, result harness.Result, agentSucceeded bool) *domain.ResumePlan {
	if agentSucceeded || task.ResumeAttempts >= MaxUsageLimitResumes {
		return nil
	}
	now := e.now().UTC()
	if result.ResumeRejected {
		// The earlier session could not be reopened; start afresh now.
		return &domain.ResumePlan{After: now}
	}
	if result.UsageLimit == nil {
		return nil
	}
	after := now.Add(usageLimitResumeFallback)
	if !result.UsageLimit.ResetsAt.IsZero() {
		after = result.UsageLimit.ResetsAt.UTC().Add(usageLimitResumeMargin)
		if after.Before(now) {
			after = now
		}
	}
	return &domain.ResumePlan{
		After:         after,
		KeepWorkspace: harness.ProfileCanResume(profile) && keepsWorkspace(profile) && result.SessionID != "",
	}
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
	terminalPayload := map[string]any{
		"state": completion.State, "exit_code": completion.ExitCode,
		"error": completion.Error, "finalize_state": completion.FinalizeState,
		"finalize_error": completion.FinalizeError,
	}
	if completion.Resume != nil {
		terminalPayload["outcome"] = domain.RunOutcomeUsageLimited
		terminalPayload["resume_after"] = completion.Resume.After
	}
	e.recordEvent(ctx, run.ID, terminalEvent, terminalPayload)
	if e.Notifier == nil {
		return nil
	}
	eventType := domain.EventRunCompleted
	message := "Redline run completed"
	if completion.State == domain.RunFailed {
		eventType = domain.EventRunFailed
		message = "Redline run failed"
	}
	data := map[string]string{
		"state": string(completion.State), "exit_code": strconv.Itoa(completion.ExitCode),
		"finalize_state": completion.FinalizeState, "error": completion.Error,
	}
	if completion.Resume != nil {
		message = "Redline run stopped at the usage limit and will resume"
		data["outcome"] = domain.RunOutcomeUsageLimited
		data["resume_after"] = completion.Resume.After.UTC().Format(time.RFC3339)
	}
	event := domain.NotificationEvent{
		Version: 1, Type: eventType, OccurredAt: completedAt,
		ProviderAccountID: run.ProviderAccountID, TaskID: task.ID, RunID: run.ID,
		Message: message, Data: data,
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
	}
}
