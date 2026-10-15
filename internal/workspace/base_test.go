package workspace_test

import (
	"context"
	"io"
	"strings"
	"sync"
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
			"GIT_SSH_COMMAND":     "ssh -i /keys/deploy -o BatchMode=yes",
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
