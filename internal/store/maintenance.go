package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// Run-to-PR provenance and branch leases for pr_maintenance tasks.
//
// A run's repository is taken from two records: the directory its workspace
// was prepared in (runs.workspace_directory, written once when the run starts)
// and the repository of its task's execution profile. Runs do not snapshot the
// profile, so the profile half reflects the profile as it is now. Requiring
// the recorded workspace to sit inside the repository's managed worktree roots
// is what keeps a profile reassigned after the run from making the run
// attributable to its new repository.

// canonicalRepositoryPath cleans a path and resolves symlinks for its longest
// existing prefix, so a workspace removed after its run still compares equal
// to the live repository path (for example /tmp versus /private/tmp on macOS).
func canonicalRepositoryPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	var missing []string
	current := cleaned
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// workspaceInsideRepository reports whether a recorded workspace directory is
// one of the repository's Redline-managed checkouts: a git worktree under
// .redline/worktrees or a DevX session under .worktrees.
func workspaceInsideRepository(workspaceDirectory, repository string) bool {
	workspaceDirectory = canonicalRepositoryPath(workspaceDirectory)
	repository = canonicalRepositoryPath(repository)
	if workspaceDirectory == "" || repository == "" {
		return false
	}
	for _, root := range []string{
		filepath.Join(repository, ".redline", "worktrees"),
		filepath.Join(repository, ".worktrees"),
	} {
		relative, err := filepath.Rel(root, workspaceDirectory)
		if err != nil || relative == "." || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

func isActiveRunState(state domain.RunState) bool {
	return state == domain.RunPreparing || state == domain.RunRunning
}

// activePRMaintenanceRun returns an active pr_maintenance run whose profile
// repository is the same as the given profile's, or "" when there is none.
func activePRMaintenanceRun(ctx context.Context, tx *sql.Tx, profileID string) (string, error) {
	var repository string
	if err := tx.QueryRowContext(ctx, `SELECT repository FROM execution_profiles WHERE id = ?`, profileID).
		Scan(&repository); err != nil {
		return "", fmt.Errorf("read maintenance profile repository: %w", err)
	}
	want := canonicalRepositoryPath(repository)
	rows, err := tx.QueryContext(ctx, `SELECT r.id, p.repository FROM runs r
JOIN tasks t ON t.id = r.task_id
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE t.task_kind = ? AND r.state IN ('preparing', 'running')`, domain.KindPRMaintenance)
	if err != nil {
		return "", fmt.Errorf("check active maintenance runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID, runRepository string
		if err := rows.Scan(&runID, &runRepository); err != nil {
			return "", fmt.Errorf("scan active maintenance run: %w", err)
		}
		if canonicalRepositoryPath(runRepository) == want {
			return runID, nil
		}
	}
	return "", rows.Err()
}

// ActivePRMaintenanceRun returns the ID of an active pr_maintenance run in the
// same repository as the given profile, or "" when there is none. The
// scheduler uses it to skip a blocked maintenance task instead of selecting
// it and losing the slot to an admission conflict.
func (d *DB) ActivePRMaintenanceRun(ctx context.Context, profileID string) (string, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", fmt.Errorf("begin maintenance run check: %w", err)
	}
	defer tx.Rollback()
	return activePRMaintenanceRun(ctx, tx, profileID)
}

func releaseRunBranchLeases(ctx context.Context, tx *sql.Tx, runID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM branch_leases WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("release branch leases for run %q: %w", runID, err)
	}
	return nil
}

type branchRun struct {
	runID, taskID, taskName string
	taskKind                domain.TaskKind
	state                   domain.RunState
	workspaceDirectory      string
	workspace               domain.Workspace
	profileRepository       string
	workspaceProvider       string
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// branchRuns holds every run that has a claim on a head branch.
type branchRuns struct {
	// creator is the run whose ID the branch is named after under Redline's
	// managed naming conventions, if such a run exists.
	creator      branchRun
	foundCreator bool
	// active holds every active run, in ID order, that is the creator or that
	// recorded this branch in its workspace. While any exists the branch may
	// still be written by it, so it is not attributable, and it is leasable
	// only by a run that is the sole active claimant.
	active []branchRun
}

// activeOtherThan returns the first active claimant that is not runID.
func (b branchRuns) activeOtherThan(runID string) (branchRun, bool) {
	for _, item := range b.active {
		if item.runID != runID {
			return item, true
		}
	}
	return branchRun{}, false
}

// runsForBranch finds the runs with a claim on a head branch. Only Redline's
// own naming conventions identify the creator: the git-worktree provider
// creates "redline/<SafeRunName(id)>" and the devx provider creates a session
// (and branch) named "redline-<SafeRunName(id)>". A branch merely recorded in
// some run's workspace metadata, as a command or runtime-owned workspace may
// report any branch, never makes that run the creator; it only counts toward
// the "still active" refusal. Rows are read in run ID order so the outcome is
// deterministic.
func runsForBranch(ctx context.Context, q queryer, branch string) (branchRuns, error) {
	creatorID := ""
	for _, prefix := range []string{"redline/", "redline-"} {
		if id, ok := strings.CutPrefix(branch, prefix); ok && id != "" {
			creatorID = id
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.task_id, t.name, t.task_kind, r.state,
r.workspace_directory, r.workspace_metadata, p.repository, p.workspace_provider
FROM runs r JOIN tasks t ON t.id = r.task_id
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE r.id = ? OR json_extract(r.workspace_metadata, '$.branch') = ?
ORDER BY r.id`, creatorID, branch)
	if err != nil {
		return branchRuns{}, fmt.Errorf("find runs for branch: %w", err)
	}
	defer rows.Close()
	var result branchRuns
	for rows.Next() {
		var item branchRun
		var metadata string
		if err := rows.Scan(&item.runID, &item.taskID, &item.taskName, &item.taskKind, &item.state,
			&item.workspaceDirectory, &metadata, &item.profileRepository, &item.workspaceProvider); err != nil {
			return branchRuns{}, fmt.Errorf("scan run for branch: %w", err)
		}
		if metadata != "" && metadata != "{}" {
			if err := json.Unmarshal([]byte(metadata), &item.workspace); err != nil {
				return branchRuns{}, fmt.Errorf("decode run workspace: %w", err)
			}
		}
		if item.taskKind == "" {
			item.taskKind = domain.KindStandard
		}
		if creatorID != "" && item.runID == creatorID {
			result.creator, result.foundCreator = item, true
		}
		if isActiveRunState(item.state) {
			result.active = append(result.active, item)
		}
	}
	return result, rows.Err()
}

// managedWorkspaceMismatch explains why a run's recorded workspace is not the
// one its managed provider would have created for branch in repository, or
// returns "" when it is.
func managedWorkspaceMismatch(item branchRun, branch, repository string) string {
	var wantBranch, wantDirectory string
	switch item.workspaceProvider {
	case "git-worktree":
		wantBranch = domain.GitWorktreeRunBranch(item.runID)
		wantDirectory = filepath.Join(repository, ".redline", "worktrees", domain.SafeRunName(item.runID))
	case "devx":
		session := domain.DevXRunSession(item.runID)
		if item.workspace.SessionID != session {
			return fmt.Sprintf("run %s recorded DevX session %q, not %q", item.runID, item.workspace.SessionID, session)
		}
		wantBranch = session
		wantDirectory = filepath.Join(repository, ".worktrees", session)
	default:
		return fmt.Sprintf("run %s uses the %q workspace provider; only git-worktree and devx runs create attributable branches",
			item.runID, item.workspaceProvider)
	}
	if branch != wantBranch || item.workspace.Branch != branch {
		return fmt.Sprintf("run %s recorded workspace branch %q, not %q", item.runID, item.workspace.Branch, branch)
	}
	if canonicalRepositoryPath(item.workspaceDirectory) != canonicalRepositoryPath(wantDirectory) {
		return fmt.Sprintf("run %s workspace %q is not inside this repository's managed worktrees",
			item.runID, item.workspaceDirectory)
	}
	return ""
}

// AttributeBranch decides whether a pull-request head branch belongs to a
// finished standard Redline run in the given repository. It never infers from
// authorship or text: only stored run records are consulted.
func (d *DB) AttributeBranch(ctx context.Context, branch, repository string) (domain.RunAttribution, error) {
	if strings.TrimSpace(branch) == "" || strings.TrimSpace(repository) == "" {
		return domain.RunAttribution{}, fmt.Errorf("branch and repository are required")
	}
	branch, err := normalizeBranch(branch)
	if err != nil {
		return domain.RunAttribution{}, err
	}
	if !filepath.IsAbs(repository) {
		return domain.RunAttribution{}, fmt.Errorf("repository must be an absolute path")
	}
	repository = canonicalRepositoryPath(repository)
	result := domain.RunAttribution{Repository: repository}
	runs, err := runsForBranch(ctx, d.db, branch)
	if err != nil {
		return domain.RunAttribution{}, err
	}
	if !runs.foundCreator {
		result.Reason = "no Redline run created this branch"
		if len(runs.active) > 0 {
			result.Reason = fmt.Sprintf("no Redline run created this branch, and run %s that recorded it is still %s",
				runs.active[0].runID, runs.active[0].state)
		}
		return result, nil
	}
	item := runs.creator
	result.RunID, result.TaskID, result.TaskName = item.runID, item.taskID, item.taskName
	result.TaskKind, result.RunState = item.taskKind, item.state
	result.WorkspaceBranch = item.workspace.Branch
	mismatch := managedWorkspaceMismatch(item, branch, repository)
	switch {
	case mismatch != "":
		result.Reason = mismatch
	case canonicalRepositoryPath(item.profileRepository) != repository:
		result.Reason = fmt.Sprintf("run %s profile repository %q is not this repository", item.runID, item.profileRepository)
	case isActiveRunState(item.state):
		result.Reason = fmt.Sprintf("run %s is still %s", item.runID, item.state)
	case len(runs.active) > 0:
		result.Reason = fmt.Sprintf("original run still active: run %s that recorded this branch is still %s",
			runs.active[0].runID, runs.active[0].state)
	case item.taskKind != domain.KindStandard:
		result.Reason = fmt.Sprintf("run %s is a %s task, not a standard job", item.runID, item.taskKind)
	default:
		result.Attributable = true
		result.Reason = "branch was created by a finished standard run in this repository"
	}
	return result, nil
}

// AcquireBranchLease gives an active run exclusive permission to mutate a
// branch. A lease held by a run that is no longer active is stale and is taken
// over; re-acquiring by the holder is idempotent.
func (d *DB) AcquireBranchLease(ctx context.Context, repository, branch, runID string, now time.Time) (domain.BranchLease, error) {
	repository, branch, err := normalizeLeaseKey(repository, branch, runID)
	if err != nil {
		return domain.BranchLease{}, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.BranchLease{}, fmt.Errorf("begin branch lease: %w", err)
	}
	defer tx.Rollback()

	var state domain.RunState
	var workspaceDirectory, profileRepository string
	err = tx.QueryRowContext(ctx, `SELECT r.state, r.workspace_directory, p.repository FROM runs r
JOIN tasks t ON t.id = r.task_id
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE r.id = ?`, runID).Scan(&state, &workspaceDirectory, &profileRepository)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BranchLease{}, fmt.Errorf("%w: run %q", ErrNotFound, runID)
	}
	if err != nil {
		return domain.BranchLease{}, fmt.Errorf("read leasing run: %w", err)
	}
	// Only a running run can lease: a preparing run has no recorded
	// workspace yet, so its repository could not be checked below, and no
	// agent is running to use the lease.
	if state != domain.RunRunning {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s is %s; only running runs can lease branches", ErrConflict, runID, state)
	}
	if canonicalRepositoryPath(profileRepository) != repository {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s belongs to repository %q, not %q",
			ErrConflict, runID, profileRepository, repository)
	}
	if !workspaceInsideRepository(workspaceDirectory, repository) {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s workspace %q is outside repository %q",
			ErrConflict, runID, workspaceDirectory, repository)
	}
	runs, err := runsForBranch(ctx, tx, branch)
	if err != nil {
		return domain.BranchLease{}, err
	}
	if other, ok := runs.activeOtherThan(runID); ok {
		return domain.BranchLease{}, fmt.Errorf("%w: original run still active: branch %s is still being written by its original run %s",
			ErrConflict, branch, other.runID)
	}

	var holder, acquired string
	var holderState domain.RunState
	err = tx.QueryRowContext(ctx, `SELECT l.run_id, l.acquired_at, r.state FROM branch_leases l
JOIN runs r ON r.id = l.run_id WHERE l.repository = ? AND l.branch = ?`, repository, branch).
		Scan(&holder, &acquired, &holderState)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return domain.BranchLease{}, fmt.Errorf("read branch lease: %w", err)
	case holder == runID:
		acquiredAt, parseErr := parseStoredTime(acquired)
		if parseErr != nil {
			return domain.BranchLease{}, parseErr
		}
		return domain.BranchLease{Repository: repository, Branch: branch, RunID: runID, AcquiredAt: acquiredAt}, nil
	case isActiveRunState(holderState):
		return domain.BranchLease{}, fmt.Errorf("%w: branch %s is leased by active run %s", ErrConflict, branch, holder)
	default:
		if _, err := tx.ExecContext(ctx, `DELETE FROM branch_leases WHERE repository = ? AND branch = ?`,
			repository, branch); err != nil {
			return domain.BranchLease{}, fmt.Errorf("take over stale branch lease: %w", err)
		}
	}
	lease := domain.BranchLease{Repository: repository, Branch: branch, RunID: runID, AcquiredAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO branch_leases(repository, branch, run_id, acquired_at)
VALUES (?, ?, ?, ?)`, repository, branch, runID, formatTime(lease.AcquiredAt)); err != nil {
		return domain.BranchLease{}, fmt.Errorf("record branch lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.BranchLease{}, fmt.Errorf("commit branch lease: %w", err)
	}
	return lease, nil
}

// ReleaseBranchLease removes a lease. Only its holder can release it.
func (d *DB) ReleaseBranchLease(ctx context.Context, repository, branch, runID string) error {
	repository, branch, err := normalizeLeaseKey(repository, branch, runID)
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin branch lease release: %w", err)
	}
	defer tx.Rollback()
	var holder string
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM branch_leases WHERE repository = ? AND branch = ?`,
		repository, branch).Scan(&holder)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no lease on branch %s", ErrNotFound, branch)
	}
	if err != nil {
		return fmt.Errorf("read branch lease: %w", err)
	}
	if holder != runID {
		return fmt.Errorf("%w: branch %s is leased by run %s", ErrConflict, branch, holder)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM branch_leases WHERE repository = ? AND branch = ? AND run_id = ?`,
		repository, branch, runID); err != nil {
		return fmt.Errorf("release branch lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit branch lease release: %w", err)
	}
	return nil
}

// ListBranchLeases returns the leases held by a run, for tests and diagnostics.
func (d *DB) ListBranchLeases(ctx context.Context, runID string) ([]domain.BranchLease, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT repository, branch, run_id, acquired_at FROM branch_leases
WHERE run_id = ? ORDER BY repository, branch`, runID)
	if err != nil {
		return nil, fmt.Errorf("list branch leases: %w", err)
	}
	defer rows.Close()
	leases := make([]domain.BranchLease, 0)
	for rows.Next() {
		var lease domain.BranchLease
		var acquired string
		if err := rows.Scan(&lease.Repository, &lease.Branch, &lease.RunID, &acquired); err != nil {
			return nil, fmt.Errorf("scan branch lease: %w", err)
		}
		if lease.AcquiredAt, err = parseStoredTime(acquired); err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func normalizeLeaseKey(repository, branch, runID string) (string, string, error) {
	repository, branch, runID = strings.TrimSpace(repository), strings.TrimSpace(branch), strings.TrimSpace(runID)
	if repository == "" || branch == "" || runID == "" {
		return "", "", fmt.Errorf("repository, branch, and run_id are required")
	}
	if !filepath.IsAbs(repository) {
		return "", "", fmt.Errorf("repository must be an absolute path")
	}
	branch, err := normalizeBranch(branch)
	if err != nil {
		return "", "", err
	}
	return canonicalRepositoryPath(repository), branch, nil
}

// normalizeBranch accepts a short branch name or a full refs/heads/ ref and
// returns the short name. Any other ref (refs/remotes/, refs/tags/, ...) is
// not a pull-request head branch and is rejected.
func normalizeBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if short, ok := strings.CutPrefix(branch, "refs/heads/"); ok {
		branch = short
	} else if strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("branch must be a branch name or refs/heads/ ref, not %q", branch)
	}
	if branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	return branch, nil
}
