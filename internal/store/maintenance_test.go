package store_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/store"
)

var maintenanceNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// maintenanceRepo returns a repository directory that exists on disk, so
// symlink resolution (e.g. /var -> /private/var on macOS) is exercised.
func maintenanceRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(repo, ".redline", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func createProfile(t *testing.T, db *store.DB, id, provider, harness, workspace, repository string) {
	t.Helper()
	if err := db.CreateProfile(t.Context(), domain.ExecutionProfile{
		ID: id, ProviderAccountID: provider, HarnessType: harness,
		WorkspaceProvider: workspace, Repository: repository,
	}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
}

func maintenanceTask(id, profile string) domain.Task {
	return domain.Task{ID: id, Name: id, Kind: domain.KindPRMaintenance, ExecutionProfileID: profile,
		Type: domain.Recurring, MinInterval: 12 * time.Hour}
}

// startRun admits a task and records its prepared workspace, like the
// executor does, returning the run's git-worktree branch.
func startRun(t *testing.T, db *store.DB, runID, taskID, provider, repository string) string {
	t.Helper()
	if _, err := db.AdmitTaskWithLimits(t.Context(), runID, taskID, provider, "", nil,
		store.AdmissionLimits{Provider: 10}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	branch := "redline/" + runID
	if err := db.MarkRunRunning(t.Context(), runID, domain.Workspace{
		Directory: filepath.Join(repository, ".redline", "worktrees", runID), Branch: branch,
	}); err != nil {
		t.Fatal(err)
	}
	return branch
}

func TestTaskKindMigrationFromV27KeepsExistingTasksStandard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redline.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	createProfile(t, db, "p", "codex-main", "codex-cli", "devx", "/repo")
	legacy := domain.Task{ID: "legacy", Name: "Legacy", Prompt: "keep me", Priority: 70, ExecutionProfileID: "p",
		Type: domain.Recurring, MinInterval: 6 * time.Hour, DispatchTier: domain.DispatchWellBehind}
	if err := db.CreateTask(t.Context(), legacy, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate the on-disk v27 layout.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE branch_leases`,
		`ALTER TABLE tasks DROP COLUMN task_kind`,
		`DELETE FROM schema_migrations WHERE version > 27`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("downgrade %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v27: %v", err)
	}
	defer db.Close()
	got, err := db.GetTask(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != domain.KindStandard || got.Prompt != "keep me" || got.Priority != 70 ||
		got.Type != domain.Recurring || got.MinInterval != 6*time.Hour || got.DispatchTier != domain.DispatchWellBehind {
		t.Fatalf("migrated task = %#v", got)
	}
	if _, err := db.ListBranchLeases(t.Context(), "none"); err != nil {
		t.Fatalf("branch_leases missing after migration: %v", err)
	}
}

func TestPRMaintenanceTaskValidation(t *testing.T) {
	db := openTaskDB(t)
	createProfile(t, db, "worktree", "codex-main", "codex-cli", "git-worktree", "/repo")
	createProfile(t, db, "devx", "codex-main", "claude-code", "devx", "/repo")
	createProfile(t, db, "hermes", "codex-main", "hermes", "git-worktree", "/repo")
	createProfile(t, db, "runtime", "codex-main", "codex-cli", "runtime-owned", "/repo")
	createProfile(t, db, "existing", "codex-main", "codex-cli", "existing-directory", "/repo")

	oneOff := maintenanceTask("one-off", "worktree")
	oneOff.Type = domain.OneOff
	noInterval := maintenanceTask("no-interval", "worktree")
	noInterval.MinInterval = 0
	badKind := maintenanceTask("bad-kind", "worktree")
	badKind.Kind = "nightly"
	for _, test := range []struct {
		task domain.Task
		want string
	}{
		{oneOff, "must be recurring"},
		{noInterval, "positive min_interval"},
		{badKind, "kind must be standard or pr_maintenance"},
		{maintenanceTask("hermes", "hermes"), "local agent harness"},
		{maintenanceTask("runtime", "runtime"), "git-worktree or devx"},
		{maintenanceTask("existing", "existing"), "git-worktree or devx"},
	} {
		err := db.CreateTask(t.Context(), test.task, maintenanceNow)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", test.task.ID, err, test.want)
		}
	}
	// Empty prompt and prompt file are allowed; the instructions are built in.
	for _, profile := range []string{"worktree", "devx"} {
		task := maintenanceTask("ok-"+profile, profile)
		if err := db.CreateTask(t.Context(), task, maintenanceNow); err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		got, err := db.GetTask(t.Context(), task.ID)
		if err != nil || got.Kind != domain.KindPRMaintenance || got.Prompt != "" {
			t.Fatalf("task=%#v err=%v", got, err)
		}
	}
	// An empty kind is stored as standard.
	if err := db.CreateTask(t.Context(), domain.Task{ID: "plain", Name: "Plain", ExecutionProfileID: "existing", Type: domain.OneOff}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTask(t.Context(), "plain"); got.Kind != domain.KindStandard {
		t.Fatalf("plain kind = %q", got.Kind)
	}
	// Update applies the same rules, including moving to an incompatible profile.
	moved := maintenanceTask("ok-worktree", "hermes")
	if err := db.UpdateTask(t.Context(), moved, maintenanceNow); err == nil || !strings.Contains(err.Error(), "local agent harness") {
		t.Fatalf("update to hermes profile err = %v", err)
	}
	toOneOff := maintenanceTask("ok-worktree", "worktree")
	toOneOff.Type = domain.OneOff
	if err := db.UpdateTask(t.Context(), toOneOff, maintenanceNow); err == nil {
		t.Fatal("update to one_off must be rejected")
	}
	converted := domain.Task{ID: "plain", Name: "Plain", Kind: domain.KindPRMaintenance, ExecutionProfileID: "worktree",
		Type: domain.Recurring, MinInterval: time.Hour}
	if err := db.UpdateTask(t.Context(), converted, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	// A profile in use by a maintenance task cannot become incompatible.
	if err := db.UpdateProfile(t.Context(), domain.ExecutionProfile{ID: "worktree", ProviderAccountID: "codex-main",
		HarnessType: "codex-cli", WorkspaceProvider: "existing-directory", Repository: "/repo"}); err == nil ||
		!strings.Contains(err.Error(), "git-worktree or devx") {
		t.Fatalf("profile update err = %v", err)
	}
}

func TestPRMaintenanceAdmissionAllowsOneActiveRunPerRepository(t *testing.T) {
	db := openTaskDB(t)
	repoA, repoB := maintenanceRepo(t, "a"), maintenanceRepo(t, "b")
	createProfile(t, db, "a1", "provider", "codex-cli", "git-worktree", repoA)
	createProfile(t, db, "a2", "provider", "claude-code", "devx", repoA+string(filepath.Separator))
	createProfile(t, db, "b", "provider", "codex-cli", "git-worktree", repoB)
	for _, task := range []domain.Task{maintenanceTask("maint-a1", "a1"), maintenanceTask("maint-a2", "a2"), maintenanceTask("maint-b", "b"),
		{ID: "std-a", Name: "std", ExecutionProfileID: "a1", Type: domain.OneOff}} {
		if err := db.CreateTask(t.Context(), task, maintenanceNow); err != nil {
			t.Fatal(err)
		}
	}
	limits := store.AdmissionLimits{Provider: 10}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-a1", "maint-a1", "provider", "", nil, limits, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	_, err := db.AdmitTaskWithLimits(t.Context(), "run-a2", "maint-a2", "provider", "", nil, limits, maintenanceNow)
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "run-a1") {
		t.Fatalf("second maintenance run in same repository err = %v", err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-b", "maint-b", "provider", "", nil, limits, maintenanceNow); err != nil {
		t.Fatalf("other repository: %v", err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-std", "std-a", "provider", "", nil, limits, maintenanceNow); err != nil {
		t.Fatalf("standard task in same repository: %v", err)
	}
	if err := db.CompleteRun(t.Context(), "run-a1", domain.RunCompletion{State: domain.RunCompleted}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitTaskWithLimits(t.Context(), "run-a2", "maint-a2", "provider", "", nil, limits, maintenanceNow); err != nil {
		t.Fatalf("after the first run completed: %v", err)
	}
}

// leaseFixture: an original standard run that created a PR branch (finished),
// and two active maintenance runs in the same repository from different
// providers (admission allows one per repository, so the second is admitted
// as a standard task to model a competing writer).
type leaseFixture struct {
	db                    *store.DB
	repo, branch          string
	holder, rival, origin string
}

func newLeaseFixture(t *testing.T) leaseFixture {
	t.Helper()
	db := openTaskDB(t)
	repo := maintenanceRepo(t, "project")
	createProfile(t, db, "std", "provider", "codex-cli", "git-worktree", repo)
	createProfile(t, db, "maint", "provider", "codex-cli", "git-worktree", repo)
	for _, task := range []domain.Task{
		{ID: "bug-hunt", Name: "Bug hunt", ExecutionProfileID: "std", Type: domain.OneOff},
		{ID: "other", Name: "Other", ExecutionProfileID: "std", Type: domain.OneOff},
		maintenanceTask("maint", "maint"),
	} {
		if err := db.CreateTask(t.Context(), task, maintenanceNow); err != nil {
			t.Fatal(err)
		}
	}
	branch := startRun(t, db, "origin-run", "bug-hunt", "provider", repo)
	if err := db.CompleteRun(t.Context(), "origin-run", domain.RunCompletion{State: domain.RunCompleted}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	startRun(t, db, "holder-run", "maint", "provider", repo)
	startRun(t, db, "rival-run", "other", "provider", repo)
	return leaseFixture{db: db, repo: repo, branch: branch, holder: "holder-run", rival: "rival-run", origin: "origin-run"}
}

func TestBranchLeaseLifecycle(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := t.Context()
	lease, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.holder, maintenanceNow)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.holder, maintenanceNow.Add(time.Minute))
	if err != nil || !again.AcquiredAt.Equal(lease.AcquiredAt) {
		t.Fatalf("idempotent re-acquire = %#v err=%v", again, err)
	}
	if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.rival, maintenanceNow); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rival acquire err = %v", err)
	}
	if err := f.db.ReleaseBranchLease(ctx, f.repo, f.branch, f.rival); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("non-holder release err = %v", err)
	}
	if err := f.db.ReleaseBranchLease(ctx, f.repo, f.branch, f.holder); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReleaseBranchLease(ctx, f.repo, f.branch, f.holder); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double release err = %v", err)
	}
	// Completion releases the holder's leases, and a rival then takes over.
	if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.holder, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CompleteRun(ctx, f.holder, domain.RunCompletion{State: domain.RunFailed}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if leases, _ := f.db.ListBranchLeases(ctx, f.holder); len(leases) != 0 {
		t.Fatalf("completion left leases %#v", leases)
	}
	if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.rival, maintenanceNow); err != nil {
		t.Fatalf("acquire after holder completed: %v", err)
	}
	// Restart recovery releases leases too.
	if err := f.db.RecoverInterruptedRuns(ctx, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if leases, _ := f.db.ListBranchLeases(ctx, f.rival); len(leases) != 0 {
		t.Fatalf("recovery left leases %#v", leases)
	}
}

func TestBranchLeaseTakesOverStaleLease(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := t.Context()
	if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.holder, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	// Simulate a lease left behind by a run that ended without releasing it.
	if err := f.db.CompleteRun(ctx, f.holder, domain.RunCompletion{State: domain.RunCompleted}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ForceBranchLeaseForTest(ctx, f.repo, f.branch, f.holder, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	lease, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, f.rival, maintenanceNow.Add(time.Hour))
	if err != nil || lease.RunID != f.rival {
		t.Fatalf("stale takeover lease=%#v err=%v", lease, err)
	}
}

func TestBranchLeaseRefusesIneligibleRequesters(t *testing.T) {
	f := newLeaseFixture(t)
	ctx := t.Context()
	other := maintenanceRepo(t, "other")
	createProfile(t, f.db, "elsewhere", "provider-2", "codex-cli", "git-worktree", other)
	if err := f.db.CreateTask(ctx, domain.Task{ID: "elsewhere", Name: "x", ExecutionProfileID: "elsewhere", Type: domain.OneOff}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	startRun(t, f.db, "elsewhere-run", "elsewhere", "provider-2", other)

	for _, test := range []struct {
		name, runID string
		want        error
	}{
		{"nonexistent run", "missing-run", store.ErrNotFound},
		{"inactive run", f.origin, store.ErrConflict},
		{"run from another project", "elsewhere-run", store.ErrConflict},
	} {
		if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, test.runID, maintenanceNow); !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", test.name, err, test.want)
		}
	}
	if _, err := f.db.AcquireBranchLease(ctx, f.repo, f.branch, "", maintenanceNow); err == nil {
		t.Error("empty run id accepted")
	}
	if _, err := f.db.AcquireBranchLease(ctx, "relative/repo", f.branch, f.holder, maintenanceNow); err == nil {
		t.Error("relative repository accepted")
	}
}

func TestBranchLeaseRefusedWhileOriginalRunIsActive(t *testing.T) {
	db := openTaskDB(t)
	repo := maintenanceRepo(t, "project")
	createProfile(t, db, "std", "provider", "codex-cli", "git-worktree", repo)
	createProfile(t, db, "maint", "provider", "codex-cli", "git-worktree", repo)
	for _, task := range []domain.Task{{ID: "bug-hunt", Name: "b", ExecutionProfileID: "std", Type: domain.OneOff}, maintenanceTask("maint", "maint")} {
		if err := db.CreateTask(t.Context(), task, maintenanceNow); err != nil {
			t.Fatal(err)
		}
	}
	branch := startRun(t, db, "origin-run", "bug-hunt", "provider", repo)
	startRun(t, db, "maint-run", "maint", "provider", repo)
	_, err := db.AcquireBranchLease(t.Context(), repo, branch, "maint-run", maintenanceNow)
	if !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "original run origin-run") {
		t.Fatalf("err = %v", err)
	}
}

func TestAttributeBranchRefusalReasons(t *testing.T) {
	db := openTaskDB(t)
	ctx := t.Context()
	repo := maintenanceRepo(t, "project")
	other := maintenanceRepo(t, "other")
	createProfile(t, db, "std", "p1", "codex-cli", "git-worktree", repo)
	createProfile(t, db, "moved", "p2", "codex-cli", "git-worktree", repo)
	createProfile(t, db, "maint", "p3", "codex-cli", "git-worktree", repo)
	createProfile(t, db, "elsewhere", "p4", "codex-cli", "git-worktree", other)
	for _, task := range []domain.Task{
		{ID: "done", Name: "Bug hunt", ExecutionProfileID: "std", Type: domain.OneOff},
		{ID: "active", Name: "Active", ExecutionProfileID: "std", Type: domain.Recurring},
		{ID: "moved", Name: "Moved", ExecutionProfileID: "moved", Type: domain.OneOff},
		maintenanceTask("maint", "maint"),
		{ID: "elsewhere", Name: "Elsewhere", ExecutionProfileID: "elsewhere", Type: domain.OneOff},
		{ID: "renamed", Name: "Renamed", ExecutionProfileID: "elsewhere", Type: domain.OneOff},
	} {
		if err := db.CreateTask(ctx, task, maintenanceNow); err != nil {
			t.Fatal(err)
		}
	}
	finish := func(runID string) {
		t.Helper()
		if err := db.CompleteRun(ctx, runID, domain.RunCompletion{State: domain.RunCompleted}, maintenanceNow); err != nil {
			t.Fatal(err)
		}
	}
	doneBranch := startRun(t, db, "run-done", "done", "p1", repo)
	finish("run-done")
	activeBranch := startRun(t, db, "run-active", "active", "p1", repo)
	maintBranch := startRun(t, db, "run-maint", "maint", "p3", repo)
	finish("run-maint")
	elsewhereBranch := startRun(t, db, "run-elsewhere", "elsewhere", "p4", other)
	finish("run-elsewhere")
	// A run made in the other repository, whose profile was later pointed at
	// this repository: its recorded workspace still says "other".
	movedBranch := startRun(t, db, "run-moved", "moved", "p2", other)
	finish("run-moved")
	// A run whose recorded branch differs from the redline/<id> convention.
	if _, err := db.AdmitTaskWithLimits(ctx, "run-renamed", "renamed", "p4", "", nil, store.AdmissionLimits{Provider: 10}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRunRunning(ctx, "run-renamed", domain.Workspace{
		Directory: filepath.Join(other, ".redline", "worktrees", "run-renamed"), Branch: "feature/renamed",
	}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, branch, repository string
		attributable             bool
		reason                   string
	}{
		{"finished standard run", doneBranch, repo, true, "finished standard run"},
		{"trailing slash repository", doneBranch, repo + string(filepath.Separator), true, ""},
		{"unknown branch", "feature/human", repo, false, "no Redline run"},
		{"unknown run id", "redline/nope", repo, false, "no Redline run"},
		{"branch mismatch", "redline/run-renamed", other, false, "recorded workspace branch"},
		{"other repository", elsewhereBranch, repo, false, "not inside this repository"},
		{"profile reassigned after run", movedBranch, repo, false, "not inside this repository"},
		{"active run", activeBranch, repo, false, "still running"},
		{"maintenance run", maintBranch, repo, false, "pr_maintenance"},
	} {
		got, err := db.AttributeBranch(ctx, test.branch, test.repository)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if got.Attributable != test.attributable || !strings.Contains(got.Reason, test.reason) {
			t.Errorf("%s: attribution = %#v", test.name, got)
		}
	}
	// Reassigning the profile before asking does not move the run either.
	if err := db.CompleteRun(ctx, "run-renamed", domain.RunCompletion{State: domain.RunCompleted}, maintenanceNow); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateProfile(ctx, domain.ExecutionProfile{ID: "elsewhere", ProviderAccountID: "p4",
		HarnessType: "codex-cli", WorkspaceProvider: "git-worktree", Repository: repo}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.AttributeBranch(ctx, elsewhereBranch, repo); got.Attributable {
		t.Fatalf("reassigned profile made run attributable: %#v", got)
	}
	if _, err := db.AttributeBranch(ctx, doneBranch, "relative"); err == nil {
		t.Fatal("relative repository accepted")
	}
}
