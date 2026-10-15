package workspace_test

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
	"github.com/croutoncreations/redline/internal/workspace"
)

func environmentValue(environment []string, name string) (string, bool) {
	for _, variable := range environment {
		if key, value, ok := strings.Cut(variable, "="); ok && key == name {
			return value, true
		}
	}
	return "", false
}

// Git network commands must never wait on a credential or host-key prompt.
func TestSyncBaseDisablesPromptsForNetworkCommands(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /keys/deploy")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	repo := t.TempDir()
	network := map[string][]string{}
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		args := strings.Join(command.Args, " ")
		switch {
		case strings.HasSuffix(args, "remote get-url origin"):
			_, _ = io.WriteString(command.Stdout, "git@example.com:o/r.git\n")
		case strings.Contains(args, "symbolic-ref"):
			return 1, nil
		case strings.Contains(args, "ls-remote"):
			network["ls-remote"] = command.Env
			_, _ = io.WriteString(command.Stdout, "ref: refs/heads/main\tHEAD\n")
		case strings.Contains(args, " fetch "):
			network["fetch"] = command.Env
		case strings.Contains(args, "rev-parse"):
			if command.Env != nil {
				t.Errorf("local command %q got a network environment", args)
			}
			_, _ = io.WriteString(command.Stdout, "abc123\n")
		}
		return 0, nil
	}}
	if _, err := workspace.SyncBase(t.Context(), runner, repo, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fetch", "ls-remote"} {
		environment, ok := network[name]
		if !ok {
			t.Fatalf("%s was not run", name)
		}
		for key, want := range map[string]string{
			"GIT_TERMINAL_PROMPT": "0",
			"GCM_INTERACTIVE":     "never",
			"GIT_SSH_COMMAND":     "ssh -o BatchMode=yes -i /keys/deploy",
		} {
			if got, _ := environmentValue(environment, key); got != want {
				t.Errorf("%s: %s=%q, want %q", name, key, got, want)
			}
		}
		if _, ok := environmentValue(environment, "PATH"); !ok {
			t.Errorf("%s: environment dropped PATH", name)
		}
	}
}

// fetchEnvironment runs SyncBase against a fake origin whose core.sshCommand
// is coreSSHCommand ("" for unset) and returns the fetch's environment.
func fetchEnvironment(t *testing.T, coreSSHCommand string) []string {
	t.Helper()
	var fetch []string
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		args := strings.Join(command.Args, " ")
		switch {
		case strings.HasSuffix(args, "remote get-url origin"):
			_, _ = io.WriteString(command.Stdout, "git@example.com:o/r.git\n")
		case strings.HasSuffix(args, "config --get core.sshCommand"):
			if command.Env != nil {
				t.Errorf("config read got a network environment")
			}
			if coreSSHCommand == "" {
				return 1, nil
			}
			_, _ = io.WriteString(command.Stdout, coreSSHCommand+"\n")
		case strings.Contains(args, " fetch "):
			fetch = command.Env
		case strings.Contains(args, "rev-parse"):
			_, _ = io.WriteString(command.Stdout, "abc123\n")
		}
		return 0, nil
	}}
	if _, err := workspace.SyncBase(t.Context(), runner, t.TempDir(), "main"); err != nil {
		t.Fatal(err)
	}
	if fetch == nil {
		t.Fatal("fetch was not run")
	}
	return fetch
}

// Batch mode is added without overriding a configured deploy key, following
// git's precedence: GIT_SSH_COMMAND, core.sshCommand, then GIT_SSH.
func TestSyncBaseKeepsConfiguredSSHCommand(t *testing.T) {
	for _, test := range []struct {
		name, envCommand, gitSSH, coreCommand string
		want                                  string
		wantSet                               bool
	}{
		{name: "nothing configured", want: "ssh -o BatchMode=yes", wantSet: true},
		{name: "existing GIT_SSH_COMMAND", envCommand: "ssh -o BatchMode=no -i /keys/env",
			want: "ssh -o BatchMode=yes -o BatchMode=no -i /keys/env", wantSet: true},
		{name: "GIT_SSH_COMMAND wins over core.sshCommand", envCommand: "ssh -i /keys/env", coreCommand: "ssh -i /keys/core",
			want: "ssh -o BatchMode=yes -i /keys/env", wantSet: true},
		{name: "core.sshCommand", coreCommand: "ssh -i /keys/deploy -o BatchMode=no",
			want: "ssh -o BatchMode=yes -i /keys/deploy -o BatchMode=no", wantSet: true},
		{name: "core.sshCommand wins over GIT_SSH", gitSSH: "/usr/local/bin/deploy-ssh", coreCommand: "ssh -i /keys/core",
			want: "ssh -o BatchMode=yes -i /keys/core", wantSet: true},
		{name: "GIT_SSH left alone", gitSSH: "/usr/local/bin/deploy-ssh", wantSet: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GIT_SSH_COMMAND", test.envCommand)
			t.Setenv("GIT_SSH", test.gitSSH)
			environment := fetchEnvironment(t, test.coreCommand)
			got, ok := environmentValue(environment, "GIT_SSH_COMMAND")
			if ok != test.wantSet || got != test.want {
				t.Fatalf("GIT_SSH_COMMAND=%q set=%v, want %q set=%v", got, ok, test.want, test.wantSet)
			}
			if test.gitSSH != "" {
				if got, _ := environmentValue(environment, "GIT_SSH"); got != test.gitSSH {
					t.Fatalf("GIT_SSH=%q, want %q", got, test.gitSSH)
				}
			}
		})
	}
}

// ssh honours the first occurrence of an option, so the BatchMode=yes put
// right after the program name beats a BatchMode=no in the configured command.
func TestSyncBaseBatchModeWinsInRealSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=no")
	t.Setenv("GIT_SSH", "")
	command, _ := environmentValue(fetchEnvironment(t, ""), "GIT_SSH_COMMAND")
	out, err := exec.Command("sh", "-c", command+" -G example.invalid").Output()
	if err != nil {
		t.Fatalf("%s -G: %v", command, err)
	}
	if !strings.Contains(string(out), "\nbatchmode yes\n") {
		t.Fatalf("%q resolves batchmode to no:\n%s", command, out)
	}
}

// Overlapping fetches of one repository would race on git's ref locks; both
// must succeed.
func TestConcurrentSyncBaseOnOneRepositorySucceeds(t *testing.T) {
	upstream, local := originFixture(t)
	advanced := commitFile(t, upstream, "b.txt", "two")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	shas := make([]string, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			synced, err := workspace.SyncBase(context.Background(), nil, local, "main")
			shas[i], errs[i] = synced.SHA, err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil || shas[i] != advanced {
			t.Fatalf("sync %d: sha=%s err=%v", i, shas[i], err)
		}
	}
}

// The scheduler's revision read reuses a recent fetch, but Prepare always
// fetches origin as it is now.
func TestRevisionResolverCachesFetchButPrepareFetchesFresh(t *testing.T) {
	upstream, local := originFixture(t)
	first := commitFile(t, upstream, "b.txt", "two")
	profile := domain.ExecutionProfile{Repository: local, BaseBranch: "main", WorkspaceProvider: "git-worktree"}
	revision, err := (workspace.GitRevisionResolver{}).Resolve(t.Context(), profile)
	if err != nil || revision != first {
		t.Fatalf("revision=%s err=%v", revision, err)
	}
	second := commitFile(t, upstream, "c.txt", "three")
	fetches := 0
	counting := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		if strings.Contains(strings.Join(command.Args, " "), " fetch ") {
			fetches++
		}
		return redprocess.ExecRunner{}.Run(context.Background(), command)
	}}
	revision, err = (workspace.GitRevisionResolver{Runner: counting}).Resolve(t.Context(), profile)
	if err != nil || revision != first || fetches != 0 {
		t.Fatalf("cached revision=%s fetches=%d err=%v", revision, fetches, err)
	}
	prepared, err := workspace.Manager{Runner: counting}.Prepare(t.Context(), "run-fresh", "Task", profile)
	if err != nil {
		t.Fatal(err)
	}
	if fetches != 1 || prepared.Metadata["base_sha"] != second {
		t.Fatalf("prepare fetches=%d base_sha=%s, want fresh %s", fetches, prepared.Metadata["base_sha"], second)
	}
}

// originRunner fakes a repository with an origin; onFetch handles the fetch.
func originRunner(onFetch func() (int, error)) *fakeRunner {
	return &fakeRunner{run: func(command redprocess.Command) (int, error) {
		args := strings.Join(command.Args, " ")
		switch {
		case strings.HasSuffix(args, "remote get-url origin"):
			_, _ = io.WriteString(command.Stdout, "git@example.com:o/r.git\n")
		case strings.HasSuffix(args, "config --get core.sshCommand"):
			return 1, nil
		case strings.Contains(args, " fetch "):
			return onFetch()
		case strings.Contains(args, "rev-parse"):
			_, _ = io.WriteString(command.Stdout, "abc123\n")
		}
		return 0, nil
	}}
}

// A fetch that failed because the caller's context ended says nothing about
// the repository, so the next scheduling read must fetch again.
func TestCachedSyncBaseDoesNotCacheCancelledFetch(t *testing.T) {
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	var fetches atomic.Int32
	runner := originRunner(func() (int, error) {
		if fetches.Add(1) == 1 {
			cancel()
			return 0, context.Canceled
		}
		return 0, nil
	})
	if _, err := workspace.CachedSyncBase(ctx, runner, repo, "main"); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
	synced, err := workspace.CachedSyncBase(t.Context(), runner, repo, "main")
	if err != nil || synced.SHA != "abc123" || fetches.Load() != 2 {
		t.Fatalf("after cancellation sha=%s fetches=%d err=%v", synced.SHA, fetches.Load(), err)
	}
}

// A base beginning with "-" is a ref name, never a git option.
func TestSyncBaseWithoutOriginTreatsBaseAsRef(t *testing.T) {
	repo := t.TempDir()
	var revParse string
	runner := &fakeRunner{run: func(command redprocess.Command) (int, error) {
		args := strings.Join(command.Args, " ")
		if strings.HasSuffix(args, "remote get-url origin") {
			return 2, nil
		}
		revParse = args
		return 1, nil
	}}
	_, _ = workspace.SyncBase(t.Context(), runner, repo, "--output=/tmp/x")
	if !strings.Contains(revParse, "--end-of-options --output=/tmp/x^{commit}") {
		t.Fatalf("rev-parse args = %q", revParse)
	}
}
