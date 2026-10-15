package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// syncBaseCacheTTL bounds how long a scheduler-side revision read reuses an
// earlier fetch of the same repository and base, so one scheduling cycle or a
// burst of dispatches fetches each repository at most once.
const syncBaseCacheTTL = 2 * time.Minute

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
//
// SyncBase always fetches: workspace preparation must start from origin as it
// is now, so it never uses the cache. Its result does refresh the cache that
// CachedSyncBase reads.
func SyncBase(ctx context.Context, runner redprocess.Runner, repository, configuredBase string) (BaseSync, error) {
	// Overlapping fetches of one repository can collide on git's ref locks
	// and fail, so access to a repository is serialized process-wide.
	unlock := lockRepository(repository)
	defer unlock()
	return syncBaseAndStore(ctx, runner, repository, configuredBase)
}

// CachedSyncBase is SyncBase for scheduling decisions. It reuses a result for
// the same repository and configured base that is younger than
// syncBaseCacheTTL, including a failure, so repeated candidate evaluation
// cannot turn into a fetch per candidate per cycle. The revision it reports
// can therefore be up to the TTL old; preparation fetches fresh regardless.
// Concurrent misses fetch once: the cache is checked again after taking the
// repository lock, so callers that waited behind a fetch reuse its result.
func CachedSyncBase(ctx context.Context, runner redprocess.Runner, repository, configuredBase string) (BaseSync, error) {
	if synced, err, ok := syncBaseResults.load(repository, configuredBase); ok {
		return synced, err
	}
	unlock := lockRepository(repository)
	defer unlock()
	if synced, err, ok := syncBaseResults.load(repository, configuredBase); ok {
		return synced, err
	}
	return syncBaseAndStore(ctx, runner, repository, configuredBase)
}

// syncBaseAndStore runs syncBase and caches its result. The caller holds the
// repository lock. A failure caused by the caller's own context ending
// (cancellation or deadline) says nothing about the repository, so it is not
// cached.
func syncBaseAndStore(ctx context.Context, runner redprocess.Runner, repository, configuredBase string) (BaseSync, error) {
	synced, err := syncBase(ctx, runner, repository, configuredBase)
	if err != nil && ctx.Err() != nil {
		return synced, err
	}
	syncBaseResults.store(repository, configuredBase, synced, err)
	return synced, err
}

func syncBase(ctx context.Context, runner redprocess.Runner, repository, configuredBase string) (BaseSync, error) {
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
		// --end-of-options keeps a base branch beginning with "-" from being
		// read as an option.
		sha, _, err := git.output(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
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
	if _, stderr, err := git.network(fetchCtx, "fetch", "--quiet", "--no-tags", "origin", refspec); err != nil {
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

// canonicalRepository keys per-repository state so that different spellings
// of one path (a trailing slash, /tmp versus /private/tmp) share it.
func canonicalRepository(repository string) string {
	cleaned := filepath.Clean(repository)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

var repositoryLocks sync.Map // canonical repository path -> *sync.Mutex

func lockRepository(repository string) func() {
	value, _ := repositoryLocks.LoadOrStore(canonicalRepository(repository), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

type syncBaseEntry struct {
	synced BaseSync
	err    error
	at     time.Time
}

type syncBaseCache struct {
	mu      sync.Mutex
	entries map[string]syncBaseEntry
}

var syncBaseResults = &syncBaseCache{entries: map[string]syncBaseEntry{}}

func syncBaseKey(repository, configuredBase string) string {
	return canonicalRepository(repository) + "\x00" + domain.BaseBranchName(configuredBase)
}

func (c *syncBaseCache) load(repository, configuredBase string) (BaseSync, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[syncBaseKey(repository, configuredBase)]
	if !ok || time.Since(entry.at) >= syncBaseCacheTTL {
		return BaseSync{}, nil, false
	}
	return entry.synced, entry.err, true
}

func (c *syncBaseCache) store(repository, configuredBase string, synced BaseSync, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[syncBaseKey(repository, configuredBase)] = syncBaseEntry{synced: synced, err: err, at: time.Now()}
}

// gitNetworkEnvironment is the environment for git commands that contact a
// remote. Unattended runs have no one to answer a credential or host-key
// prompt, so every prompt fails fast instead of hanging until the timeout.
//
// SSH follows git's own precedence (GIT_SSH_COMMAND, then core.sshCommand,
// then GIT_SSH) so a configured deploy key keeps working: an existing
// GIT_SSH_COMMAND or core.sshCommand gains "-o BatchMode=yes", a GIT_SSH
// program is left alone (it takes no extra options), and only when nothing is
// configured is plain "ssh -o BatchMode=yes" used. coreSSHCommand is the
// repository's core.sshCommand value, or "".
func gitNetworkEnvironment(coreSSHCommand string) []string {
	ssh := strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND"))
	switch {
	case ssh != "":
	case strings.TrimSpace(coreSSHCommand) != "":
		ssh = strings.TrimSpace(coreSSHCommand)
	case strings.TrimSpace(os.Getenv("GIT_SSH")) != "":
	default:
		ssh = "ssh"
	}
	environment := make([]string, 0, len(os.Environ())+3)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "GIT_TERMINAL_PROMPT", "GCM_INTERACTIVE", "GIT_SSH_COMMAND":
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(environment, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if ssh != "" {
		environment = append(environment, "GIT_SSH_COMMAND="+batchModeSSHCommand(ssh))
	}
	return environment
}

// batchModeSSHCommand puts "-o BatchMode=yes" right after the program name:
// ssh honours the first occurrence of an option, so a later
// "-o BatchMode=no" in the configured command cannot re-enable prompts. The
// command is a shell string; only an unquoted leading program name is split
// off. A command beginning with a quote (a program path containing spaces)
// cannot be split safely, so the option is appended instead, where an earlier
// explicit BatchMode=no would still win.
func batchModeSSHCommand(command string) string {
	const option = "-o BatchMode=yes"
	if strings.ContainsAny(command[:1], `"'`) {
		return command + " " + option
	}
	end := strings.IndexAny(command, " \t")
	if end < 0 {
		return command + " " + option
	}
	return command[:end] + " " + option + " " + strings.TrimSpace(command[end:])
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

// network runs a git command that contacts a remote, with prompts disabled.
func (g gitRunner) network(ctx context.Context, args ...string) (string, string, error) {
	return g.run(ctx, g.repository, gitNetworkEnvironment(g.coreSSHCommand(ctx)), args...)
}

// coreSSHCommand reads core.sshCommand as git would see it for this
// repository, locally and without network access; "" when it is unset.
func (g gitRunner) coreSSHCommand(ctx context.Context) string {
	value, _, err := g.output(ctx, "config", "--get", "core.sshCommand")
	if err != nil {
		return ""
	}
	return value
}

func (g gitRunner) outputIn(ctx context.Context, directory string, args ...string) (string, string, error) {
	return g.run(ctx, directory, nil, args...)
}

func (g gitRunner) run(ctx context.Context, directory string, environment []string, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	exitCode, err := g.runner.Run(ctx, redprocess.Command{
		Name: "git", Args: append([]string{"-C", directory}, args...), Env: environment,
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
	out, stderr, err := g.network(lsCtx, "ls-remote", "--symref", "origin", "HEAD")
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
