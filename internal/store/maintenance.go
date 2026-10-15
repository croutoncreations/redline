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
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// runForBranch finds the run that created a head branch. Redline names its
// branches after the run ID ("redline/<id>" for git worktrees, "redline-<id>"
// for DevX sessions), so the ID is read from the branch and every candidate is
// then checked against the branch recorded in its workspace metadata.
func runForBranch(ctx context.Context, q queryer, branch string) (branchRun, bool, error) {
	var candidates []string
	for _, prefix := range []string{"redline/", "redline-"} {
		if id, ok := strings.CutPrefix(branch, prefix); ok && id != "" {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return branchRun{}, false, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.task_id, t.name, t.task_kind, r.state,
r.workspace_directory, r.workspace_metadata, p.repository
FROM runs r JOIN tasks t ON t.id = r.task_id
JOIN execution_profiles p ON p.id = t.execution_profile_id
WHERE r.id IN (?, ?) OR json_extract(r.workspace_metadata, '$.branch') = ?`,
		candidates[0], candidates[len(candidates)-1], branch)
	if err != nil {
		return branchRun{}, false, fmt.Errorf("find run for branch: %w", err)
	}
	defer rows.Close()
	var found []branchRun
	for rows.Next() {
		var item branchRun
		var metadata string
		if err := rows.Scan(&item.runID, &item.taskID, &item.taskName, &item.taskKind, &item.state,
			&item.workspaceDirectory, &metadata, &item.profileRepository); err != nil {
			return branchRun{}, false, fmt.Errorf("scan run for branch: %w", err)
		}
		if metadata != "" && metadata != "{}" {
			if err := json.Unmarshal([]byte(metadata), &item.workspace); err != nil {
				return branchRun{}, false, fmt.Errorf("decode run workspace: %w", err)
			}
		}
		if item.taskKind == "" {
			item.taskKind = domain.KindStandard
		}
		found = append(found, item)
	}
	if err := rows.Err(); err != nil {
		return branchRun{}, false, err
	}
	for _, item := range found {
		if item.workspace.Branch == branch {
			return item, true, nil
		}
	}
	if len(found) > 0 {
		// The ID matched but the recorded branch did not; report the run so
		// the caller can explain the refusal.
		return found[0], true, nil
	}
	return branchRun{}, false, nil
}

// AttributeBranch decides whether a pull-request head branch belongs to a
// finished standard Redline run in the given repository. It never infers from
// authorship or text: only stored run records are consulted.
func (d *DB) AttributeBranch(ctx context.Context, branch, repository string) (domain.RunAttribution, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.TrimSpace(repository) == "" {
		return domain.RunAttribution{}, fmt.Errorf("branch and repository are required")
	}
	if !filepath.IsAbs(repository) {
		return domain.RunAttribution{}, fmt.Errorf("repository must be an absolute path")
	}
	repository = canonicalRepositoryPath(repository)
	result := domain.RunAttribution{Repository: repository}
	item, ok, err := runForBranch(ctx, d.db, branch)
	if err != nil {
		return domain.RunAttribution{}, err
	}
	if !ok {
		result.Reason = "no Redline run created this branch"
		return result, nil
	}
	result.RunID, result.TaskID, result.TaskName = item.runID, item.taskID, item.taskName
	result.TaskKind, result.RunState = item.taskKind, item.state
	result.WorkspaceBranch = item.workspace.Branch
	switch {
	case item.workspace.Branch != branch:
		result.Reason = fmt.Sprintf("run %s recorded workspace branch %q, not %q", item.runID, item.workspace.Branch, branch)
	case !workspaceInsideRepository(item.workspaceDirectory, repository):
		result.Reason = fmt.Sprintf("run %s workspace %q is not inside this repository's managed worktrees",
			item.runID, item.workspaceDirectory)
	case canonicalRepositoryPath(item.profileRepository) != repository:
		result.Reason = fmt.Sprintf("run %s profile repository %q is not this repository", item.runID, item.profileRepository)
	case isActiveRunState(item.state):
		result.Reason = fmt.Sprintf("run %s is still %s", item.runID, item.state)
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
	if !isActiveRunState(state) {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s is %s; only active runs can lease branches", ErrConflict, runID, state)
	}
	if canonicalRepositoryPath(profileRepository) != repository {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s belongs to repository %q, not %q",
			ErrConflict, runID, profileRepository, repository)
	}
	if workspaceDirectory != "" && !workspaceInsideRepository(workspaceDirectory, repository) {
		return domain.BranchLease{}, fmt.Errorf("%w: run %s workspace %q is outside repository %q",
			ErrConflict, runID, workspaceDirectory, repository)
	}
	original, found, err := runForBranch(ctx, tx, branch)
	if err != nil {
		return domain.BranchLease{}, err
	}
	if found && original.runID != runID && original.workspace.Branch == branch && isActiveRunState(original.state) {
		return domain.BranchLease{}, fmt.Errorf("%w: branch %s is still being written by its original run %s",
			ErrConflict, branch, original.runID)
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
	return canonicalRepositoryPath(repository), branch, nil
}
