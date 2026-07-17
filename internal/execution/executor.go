package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jfox/redline/internal/domain"
	"github.com/jfox/redline/internal/harness"
	"github.com/jfox/redline/internal/workspace"
)

type Store interface {
	MarkRunRunning(context.Context, string, domain.Workspace) error
	CompleteRun(context.Context, string, domain.RunCompletion, time.Time) error
}

type WorkspaceManager interface {
	Prepare(context.Context, string, string, domain.ExecutionProfile) (domain.Workspace, error)
	Finalize(context.Context, workspace.FinalizeRequest) error
	Cleanup(context.Context, workspace.CleanupRequest) error
}

type Harness interface {
	Run(context.Context, harness.Request) (harness.Result, error)
}

type Executor struct {
	Store           Store
	Workspaces      WorkspaceManager
	Harness         Harness
	OutputDirectory string
	Now             func() time.Time
}

func (e Executor) Execute(
	ctx context.Context,
	run domain.Run,
	task domain.Task,
	profile domain.ExecutionProfile,
) error {
	prepared, err := e.Workspaces.Prepare(ctx, run.ID, task.Name, profile)
	if err != nil {
		finalizeState := "skipped"
		finalizeError := ""
		if prepared.Directory != "" {
			if recordErr := e.Store.MarkRunRunning(ctx, run.ID, prepared); recordErr != nil {
				finalizeError = "record partially prepared workspace: " + recordErr.Error()
			} else if cleanupErr := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
				Success: false, Profile: profile, Workspace: prepared,
			}); cleanupErr != nil {
				finalizeState = "failed"
				finalizeError = cleanupErr.Error()
			} else {
				finalizeState = "cleanup_completed"
			}
		}
		return e.Store.CompleteRun(ctx, run.ID, domain.RunCompletion{
			State: domain.RunFailed, ExitCode: -1, Error: "prepare workspace: " + err.Error(),
			FinalizeState: finalizeState, FinalizeError: finalizeError,
		}, e.now())
	}
	if err := e.Store.MarkRunRunning(ctx, run.ID, prepared); err != nil {
		return e.Store.CompleteRun(ctx, run.ID, domain.RunCompletion{
			State: domain.RunFailed, ExitCode: -1, Error: "record prepared workspace: " + err.Error(),
			FinalizeState: "skipped",
		}, e.now())
	}

	harnessResult, harnessErr := e.Harness.Run(ctx, harness.Request{
		RunID: run.ID, OutputDirectory: e.OutputDirectory,
		Task: task, Profile: profile, Workspace: prepared,
	})
	agentSucceeded := harnessErr == nil && harnessResult.ExitCode == 0
	state := domain.RunCompleted
	agentError := ""
	if !agentSucceeded {
		state = domain.RunFailed
		if harnessErr != nil {
			agentError = harnessErr.Error()
		} else {
			agentError = fmt.Sprintf("harness exited with code %d", harnessResult.ExitCode)
		}
	}

	status := "completed"
	if !agentSucceeded {
		status = "failed"
	}
	finalizeState := "completed"
	var lifecycleErrors []string
	if err := e.Workspaces.Finalize(ctx, workspace.FinalizeRequest{
		RunID: run.ID, TaskID: task.ID, Status: status, ExitCode: harnessResult.ExitCode,
		OutputFile: harnessResult.OutputFile, Profile: profile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
	}
	if err := e.Workspaces.Cleanup(ctx, workspace.CleanupRequest{
		Success: agentSucceeded, Profile: profile, Workspace: prepared,
	}); err != nil {
		finalizeState = "failed"
		lifecycleErrors = append(lifecycleErrors, err.Error())
	}

	return e.Store.CompleteRun(ctx, run.ID, domain.RunCompletion{
		State: state, ExitCode: harnessResult.ExitCode,
		OutputFile: harnessResult.OutputFile, ErrorFile: harnessResult.ErrorFile,
		Error: agentError, FinalizeState: finalizeState,
		FinalizeError: strings.Join(lifecycleErrors, "; "),
	}, e.now())
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
