package store_test

import (
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

func usageLimitedTask(t *testing.T) (*store.DB, domain.Task, domain.Run, time.Time) {
	t.Helper()
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "git-worktree"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "task", Name: "Task", Prompt: "work", Priority: 50, ExecutionProfileID: profile.ID, Type: domain.OneOff}
	if err := db.CreateTask(t.Context(), task, now); err != nil {
		t.Fatal(err)
	}
	run, err := db.AdmitTask(t.Context(), "run-1", task.ID, profile.ProviderAccountID, "abc", now)
	if err != nil {
		t.Fatal(err)
	}
	after := now.Add(3 * time.Hour)
	if err := db.CompleteRun(t.Context(), run.ID, domain.RunCompletion{
		State: domain.RunFailed, ExitCode: 1, Outcome: domain.RunOutcomeUsageLimited,
		ExternalSessionID: "sess-1", FinalizeState: "deferred",
		Resume: &domain.ResumePlan{After: after, KeepWorkspace: true},
	}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, stored, run, after
}

func TestUsageLimitedCompletionKeepsTaskQueuedWithResumePlan(t *testing.T) {
	db, task, run, after := usageLimitedTask(t)
	if task.State != domain.Queued || task.ResumeAfter == nil || !task.ResumeAfter.Equal(after) ||
		task.ResumeRunID != run.ID || task.ResumeAttempts != 1 {
		t.Fatalf("task = %#v", task)
	}
	storedRun, err := db.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.External.SessionID != "sess-1" || storedRun.Outcome != domain.RunOutcomeUsageLimited {
		t.Fatalf("run = %#v err=%v", storedRun, err)
	}
	candidates, err := db.DispatchCandidates(t.Context(), "claude-main")
	if err != nil || len(candidates) != 1 || candidates[0].ID != task.ID {
		t.Fatalf("candidates = %#v err=%v", candidates, err)
	}
}

func TestResumeOnlyAllowedForFailedRuns(t *testing.T) {
	db := openTaskDB(t)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	profile := domain.ExecutionProfile{ID: "profile", ProviderAccountID: "claude-main", HarnessType: "claude-code", WorkspaceProvider: "devx"}
	if err := db.CreateProfile(t.Context(), profile, now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(t.Context(), domain.Task{ID: "task", Name: "Task", Prompt: "p", ExecutionProfileID: profile.ID, Type: domain.OneOff}, now); err != nil {
		t.Fatal(err)
	}
	run, err := db.AdmitTask(t.Context(), "run", "task", "claude-main", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(t.Context(), run.ID, domain.RunCompletion{
		State: domain.RunCompleted, Resume: &domain.ResumePlan{After: now},
	}, now); err == nil {
		t.Fatal("completed run scheduled a resume")
	}
}

func TestEditingPromptOrProfileMarksResumeFresh(t *testing.T) {
	for name, edit := range map[string]func(*domain.Task){
		"prompt":      func(task *domain.Task) { task.Prompt = "different work" },
		"prompt file": func(task *domain.Task) { task.Prompt = ""; task.PromptFile = "TASK.md" },
	} {
		t.Run(name, func(t *testing.T) {
			db, task, run, after := usageLimitedTask(t)
			edit(&task)
			if err := db.UpdateTask(t.Context(), task, after); err != nil {
				t.Fatal(err)
			}
			stored, _ := db.GetTask(t.Context(), task.ID)
			// The kept run stays linked so the next run can clean it up.
			if stored.ResumeFresh != domain.ResumeFreshPrompt || stored.ResumeRunID != run.ID || stored.ResumeAfter == nil || stored.State != domain.Queued {
				t.Fatalf("task = %#v", stored)
			}
		})
	}
	t.Run("priority only", func(t *testing.T) {
		db, task, run, after := usageLimitedTask(t)
		task.Priority = 90
		if err := db.UpdateTask(t.Context(), task, after); err != nil {
			t.Fatal(err)
		}
		stored, _ := db.GetTask(t.Context(), task.ID)
		if stored.ResumeFresh != "" || stored.ResumeRunID != run.ID {
			t.Fatalf("task = %#v", stored)
		}
	})
	t.Run("profile edited in place", func(t *testing.T) {
		db, task, _, _ := usageLimitedTask(t)
		profile, err := db.GetProfile(t.Context(), task.ExecutionProfileID)
		if err != nil {
			t.Fatal(err)
		}
		profile.PrepareCommand = "make setup"
		if err := db.UpdateProfile(t.Context(), profile); err != nil {
			t.Fatal(err)
		}
		// A later prompt edit must not downgrade the profile reason.
		task.Prompt = "other"
		if err := db.UpdateTask(t.Context(), task, time.Now()); err != nil {
			t.Fatal(err)
		}
		stored, _ := db.GetTask(t.Context(), task.ID)
		if stored.ResumeFresh != domain.ResumeFreshProfile {
			t.Fatalf("task = %#v", stored)
		}
	})
}

func TestNextRunCompletionReleasesKeptWorkspace(t *testing.T) {
	db, task, kept, after := usageLimitedTask(t)
	next, err := db.AdmitTask(t.Context(), "run-2", task.ID, "claude-main", "abc", after)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteRun(t.Context(), next.ID, domain.RunCompletion{State: domain.RunCompleted, FinalizeState: "completed"}, after.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	storedKept, _ := db.GetRun(t.Context(), kept.ID)
	if storedKept.FinalizeState != domain.FinalizeContinued {
		t.Fatalf("kept run = %#v", storedKept)
	}
}

func TestDisablingWaitingTaskPreservesKeptWorkspace(t *testing.T) {
	db, task, kept, after := usageLimitedTask(t)
	if err := db.SetTaskControl(t.Context(), task.ID, "disable", after); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetTask(t.Context(), task.ID)
	storedKept, _ := db.GetRun(t.Context(), kept.ID)
	if stored.State != domain.Disabled || stored.ResumeRunID != "" || stored.ResumeAfter != nil {
		t.Fatalf("task = %#v", stored)
	}
	if storedKept.FinalizeState != domain.FinalizePreserved || storedKept.FinalizeError == "" {
		t.Fatalf("kept run = %#v", storedKept)
	}
}

func TestServiceRestartRecoveryClearsResumePlan(t *testing.T) {
	db, task, kept, after := usageLimitedTask(t)
	if _, err := db.AdmitTask(t.Context(), "run-2", task.ID, "claude-main", "abc", after); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverInterruptedRuns(t.Context(), after.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.GetTask(t.Context(), task.ID)
	if stored.State != domain.Failed || stored.ResumeAfter != nil || stored.ResumeRunID != "" || stored.ResumeAttempts != 0 {
		t.Fatalf("task = %#v", stored)
	}
	// The interrupted run may have been mid-way through the kept workspace.
	if storedKept, _ := db.GetRun(t.Context(), kept.ID); storedKept.FinalizeState != domain.FinalizePreserved {
		t.Fatalf("kept run = %#v", storedKept)
	}
	if err := db.SetTaskControl(t.Context(), task.ID, "retry", after.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.GetTask(t.Context(), task.ID)
	if stored.State != domain.Queued || stored.ResumeAfter != nil {
		t.Fatalf("retried task = %#v", stored)
	}
}
