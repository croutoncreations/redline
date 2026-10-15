package workspace

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

// Base sync outcomes recorded in Workspace.Metadata["base_sync"].
const (
	BaseSyncOrigin   = "origin"
	BaseSyncNoOrigin = "no-origin"
)

// fetchTimeout bounds one origin fetch so an unreachable remote cannot stall
// dispatch or workspace preparation indefinitely.
const fetchTimeout = 2 * time.Minute

// BaseSync describes the commit a fresh workspace starts from.
type BaseSync struct {
	// Ref is what the workspace was created from, such as "origin/main".
	Ref string
	// SHA is the commit Ref resolved to.
	SHA string
	// Sync is BaseSyncOrigin or BaseSyncNoOrigin.
	Sync string
}

func (b BaseSync) metadata() map[string]string {
	return map[string]string{"base_ref": b.Ref, "base_sha": b.SHA, "base_sync": b.Sync}
}

// SyncBase resolves the base a fresh workspace must start from.
//
// When the repository has an "origin" remote, the base branch is fetched and
// the workspace starts from origin/<base>: a stale local branch must never
// become the starting point of unattended work. Any failure to fetch or verify
// that ref is an error rather than a silent fallback. Only a repository with
// no origin at all uses the local base. An empty configured base resolves to
// origin's default branch. Fetching only updates remote-tracking refs; the
// main checkout's branch and working tree are never touched.
func SyncBase(ctx context.Context, runner redprocess.Runner, repository, configuredBase string) (BaseSync, error) {
	if runner == nil {
		runner = redprocess.ExecRunner{}
	}
	git := gitRunner{runner: runner, repository: repository}
	name := domain.BaseBranchName(configuredBase)
	hasOrigin, err := git.hasOrigin(ctx)
	if err != nil {
		return BaseSync{}, err
	}
	if !hasOrigin {
		ref := name
		if ref == "" {
			ref = "HEAD"
		}
		sha, _, err := git.output(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err != nil {
			return BaseSync{}, fmt.Errorf("no origin remote and local base %q could not be resolved in %s: %w", ref, repository, err)
		}
		return BaseSync{Ref: ref, SHA: sha, Sync: BaseSyncNoOrigin}, nil
	}
	if name == "" {
		resolved, err := git.originDefaultBranch(ctx)
		if err != nil {
			return BaseSync{}, err
		}
		name = resolved
	}
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	refspec := "+refs/heads/" + name + ":refs/remotes/origin/" + name
	if _, stderr, err := git.output(fetchCtx, "fetch", "--quiet", "--no-tags", "origin", refspec); err != nil {
		return BaseSync{}, fmt.Errorf("origin present but fetch failed: git fetch origin %s in %s: %v%s",
			name, repository, err, withDetail(stderr))
	}
	ref := "origin/" + name
	sha, stderr, err := git.output(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref+"^{commit}")
	if err != nil {
		return BaseSync{}, fmt.Errorf("origin present but %s could not be verified after fetch in %s: %v%s",
			ref, repository, err, withDetail(stderr))
	}
	return BaseSync{Ref: ref, SHA: sha, Sync: BaseSyncOrigin}, nil
}

type gitRunner struct {
	runner     redprocess.Runner
	repository string
}

// hasOrigin distinguishes a repository without an origin remote (git exits
// nonzero) from a failure to run git at all, which is an error.
func (g gitRunner) hasOrigin(ctx context.Context) (bool, error) {
	var stdout bytes.Buffer
	exitCode, err := g.runner.Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", g.repository, "remote", "get-url", "origin"},
		Stdout: &stdout, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		return false, fmt.Errorf("inspect origin remote in %s: %w", g.repository, err)
	}
	return exitCode == 0 && strings.TrimSpace(stdout.String()) != "", nil
}

// output runs git in the repository and returns trimmed stdout and stderr.
// A nonzero exit is an error.
func (g gitRunner) output(ctx context.Context, args ...string) (string, string, error) {
	return g.outputIn(ctx, g.repository, args...)
}

func (g gitRunner) outputIn(ctx context.Context, directory string, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := g.runner.Run(ctx, redprocess.Command{
		Name: "git", Args: append([]string{"-C", directory}, args...),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		return "", strings.TrimSpace(stderr.String()), err
	}
	if exitCode != 0 {
		return "", strings.TrimSpace(stderr.String()), fmt.Errorf("git %s exited with code %d", args[0], exitCode)
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), nil
}

// originDefaultBranch reads origin's default branch from the remote-tracking
// symbolic ref, then from the remote itself.
func (g gitRunner) originDefaultBranch(ctx context.Context) (string, error) {
	if ref, _, err := g.output(ctx, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if name := domain.BaseBranchName(ref); name != "" && name != ref {
			return name, nil
		}
	}
	lsCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	out, stderr, err := g.output(lsCtx, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", fmt.Errorf("origin present but its default branch could not be read in %s: %v%s",
			g.repository, err, withDetail(stderr))
	}
	for _, line := range strings.Split(out, "\n") {
		target, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		target, _, _ = strings.Cut(target, "\t")
		if name := domain.BaseBranchName(strings.TrimSpace(target)); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("origin present but reports no default branch in %s; set the profile base branch", g.repository)
}

func withDetail(stderr string) string {
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}
