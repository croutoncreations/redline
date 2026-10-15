package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/workspace"
)

// These tests use real git repositories: the behavior under test is how git
// refs move, which a fake runner cannot prove.

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Redline Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Redline Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "--quiet", "-m", "change "+name)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

// originFixture returns an upstream repository and a local clone of it whose
// main branch is NOT updated when the upstream advances.
func originFixture(t *testing.T) (upstream, local string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	upstream = filepath.Join(root, "upstream")
	local = filepath.Join(root, "local")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, upstream, "init", "--quiet", "--initial-branch=main")
	commitFile(t, upstream, "a.txt", "one")
	gitCmd(t, root, "clone", "--quiet", upstream, local)
	return upstream, local
}

func TestSyncBaseUsesFetchedOriginWhenLocalBaseIsStale(t *testing.T) {
	upstream, local := originFixture(t)
	staleLocal := gitCmd(t, local, "rev-parse", "main")
	advanced := commitFile(t, upstream, "b.txt", "two")

	resolver := workspace.GitRevisionResolver{}
	revision, err := resolver.Resolve(t.Context(), domain.ExecutionProfile{Repository: local, BaseBranch: "main", WorkspaceProvider: "git-worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if revision != advanced || revision == staleLocal {
		t.Fatalf("revision = %s, want fetched origin %s (stale local %s)", revision, advanced, staleLocal)
	}

	prepared, err := workspace.Manager{}.Prepare(t.Context(), "run-fresh", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "git-worktree", Repository: local, BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitCmd(t, prepared.Directory, "rev-parse", "HEAD"); head != advanced {
		t.Fatalf("worktree HEAD = %s, want origin %s", head, advanced)
	}
	if prepared.Metadata["base_sync"] != workspace.BaseSyncOrigin || prepared.Metadata["base_ref"] != "origin/main" ||
		prepared.Metadata["base_sha"] != advanced {
		t.Fatalf("metadata = %#v", prepared.Metadata)
	}
	// The main checkout is untouched: still on main at the stale commit.
	if got := gitCmd(t, local, "rev-parse", "main"); got != staleLocal {
		t.Fatalf("local main moved to %s", got)
	}
	if branch := gitCmd(t, local, "rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Fatalf("main checkout switched to %s", branch)
	}
	// The run branch does not track the base, so a bare push cannot hit main.
	if upstreamRef, err := exec.Command("git", "-C", prepared.Directory, "rev-parse", "--abbrev-ref", "@{upstream}").Output(); err == nil {
		t.Fatalf("run branch tracks %s", upstreamRef)
	}
}

func TestRevisionForInPlaceWorkspaceReadsLocalBase(t *testing.T) {
	upstream, local := originFixture(t)
	localHead := gitCmd(t, local, "rev-parse", "main")
	commitFile(t, upstream, "b.txt", "two")
	revision, err := (workspace.GitRevisionResolver{}).Resolve(t.Context(), domain.ExecutionProfile{
		Repository: local, BaseBranch: "main", WorkspaceProvider: "existing-directory",
	})
	if err != nil || revision != localHead {
		t.Fatalf("revision = %q err = %v, want local %s", revision, err, localHead)
	}
}

func TestSyncBaseNormalizesOriginPrefixedBase(t *testing.T) {
	upstream, local := originFixture(t)
	advanced := commitFile(t, upstream, "b.txt", "two")
	for _, base := range []string{"origin/main", "refs/remotes/origin/main", "refs/heads/main"} {
		synced, err := workspace.SyncBase(t.Context(), nil, local, base)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if synced.Ref != "origin/main" || synced.SHA != advanced {
			t.Fatalf("%s: synced = %#v", base, synced)
		}
	}
}

func TestSyncBaseResolvesEmptyBaseFromOriginDefaultBranch(t *testing.T) {
	upstream, local := originFixture(t)
	gitCmd(t, upstream, "checkout", "--quiet", "-b", "trunk")
	trunk := commitFile(t, upstream, "t.txt", "trunk")
	// Make trunk the upstream default and forget the clone's cached default.
	gitCmd(t, upstream, "symbolic-ref", "HEAD", "refs/heads/trunk")
	gitCmd(t, local, "remote", "set-head", "origin", "--delete")
	synced, err := workspace.SyncBase(t.Context(), nil, local, "")
	if err != nil {
		t.Fatal(err)
	}
	if synced.Ref != "origin/trunk" || synced.SHA != trunk || synced.Sync != workspace.BaseSyncOrigin {
		t.Fatalf("synced = %#v, want origin/trunk at %s", synced, trunk)
	}
}

func TestSyncBaseFailsWhenOriginCannotBeFetched(t *testing.T) {
	_, local := originFixture(t)
	gitCmd(t, local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
	_, err := workspace.SyncBase(t.Context(), nil, local, "main")
	if err == nil || !strings.Contains(err.Error(), "origin present but fetch failed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (workspace.GitRevisionResolver{}).Resolve(t.Context(), domain.ExecutionProfile{
		Repository: local, BaseBranch: "main", WorkspaceProvider: "devx",
	}); err == nil {
		t.Fatal("revision must be unknown, not the stale local base, when origin cannot be fetched")
	}
	_, err = workspace.Manager{}.Prepare(t.Context(), "run-offline", "Task", domain.ExecutionProfile{
		WorkspaceProvider: "git-worktree", Repository: local, BaseBranch: "main",
	})
	if err == nil || !strings.Contains(err.Error(), "origin present but fetch failed") {
		t.Fatalf("prepare err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(local, ".redline", "worktrees", "run-offline")); !os.IsNotExist(statErr) {
		t.Fatalf("worktree was created from a stale base: %v", statErr)
	}
}

func TestSyncBaseFallsBackToLocalOnlyWithoutOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	gitCmd(t, repo, "init", "--quiet", "--initial-branch=main")
	head := commitFile(t, repo, "a.txt", "one")
	synced, err := workspace.SyncBase(context.Background(), nil, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if synced.Ref != "main" || synced.SHA != head || synced.Sync != workspace.BaseSyncNoOrigin {
		t.Fatalf("synced = %#v", synced)
	}
	revision, err := (workspace.GitRevisionResolver{}).Resolve(t.Context(), domain.ExecutionProfile{Repository: repo, WorkspaceProvider: "git-worktree"})
	if err != nil || revision != head {
		t.Fatalf("revision = %q err = %v", revision, err)
	}
}

// devxLikeRunner runs git for real and emulates `devx session create`, which
// creates the session branch from the main checkout's HEAD (see devx
// session/worktree.go), optionally adding a commit of its own.
type devxLikeRunner struct {
	t          *testing.T
	repository string
	ownCommit  bool
}

func (r devxLikeRunner) Run(ctx context.Context, command redprocess.Command) (int, error) {
	if command.Name != "devx" {
		return redprocess.ExecRunner{}.Run(ctx, command)
	}
	session := command.Args[2]
	directory := filepath.Join(r.repository, ".worktrees", session)
	gitCmd(r.t, r.repository, "worktree", "add", "--quiet", "-b", session, directory)
	if r.ownCommit {
		commitFile(r.t, directory, "local.txt", "session work")
	}
	return 0, nil
}

func TestDevXSessionFastForwardsOntoFetchedOrigin(t *testing.T) {
	upstream, local := originFixture(t)
	advanced := commitFile(t, upstream, "b.txt", "two")
	prepared, err := workspace.Manager{Runner: devxLikeRunner{t: t, repository: local}}.Prepare(
		t.Context(), "run-1", "Task", domain.ExecutionProfile{WorkspaceProvider: "devx", Repository: local, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if head := gitCmd(t, prepared.Directory, "rev-parse", "HEAD"); head != advanced {
		t.Fatalf("session HEAD = %s, want %s", head, advanced)
	}
	if prepared.Branch != "redline-run-1" || prepared.Metadata["base_sync"] != workspace.BaseSyncOrigin {
		t.Fatalf("workspace = %#v", prepared)
	}
}

func TestDevXSessionWithOwnCommitsIsNotMoved(t *testing.T) {
	upstream, local := originFixture(t)
	commitFile(t, upstream, "b.txt", "two")
	prepared, err := workspace.Manager{Runner: devxLikeRunner{t: t, repository: local, ownCommit: true}}.Prepare(
		t.Context(), "run-1", "Task", domain.ExecutionProfile{WorkspaceProvider: "devx", Repository: local, BaseBranch: "main"})
	if err == nil || !strings.Contains(err.Error(), "not on origin/main") {
		t.Fatalf("err = %v", err)
	}
	if prepared.SessionID != "redline-run-1" {
		t.Fatalf("failed preparation must still report the session for cleanup: %#v", prepared)
	}
}
