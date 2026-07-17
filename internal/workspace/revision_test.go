package workspace_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
	"github.com/jfox/redline/internal/workspace"
)

func TestGitRevisionResolverReadsConfiguredBaseBranch(t *testing.T) {
	runner := &revisionRunner{}
	resolver := workspace.GitRevisionResolver{Runner: runner}
	revision, err := resolver.Resolve(context.Background(), domain.ExecutionProfile{
		Repository: "/repo", BaseBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revision != "abc123" || runner.command.Name != "git" ||
		strings.Join(runner.command.Args, " ") != "-C /repo rev-parse main" {
		t.Fatalf("revision=%q command=%#v", revision, runner.command)
	}
}

type revisionRunner struct{ command redprocess.Command }

func (r *revisionRunner) Run(_ context.Context, command redprocess.Command) (int, error) {
	r.command = command
	_, _ = io.WriteString(command.Stdout, "abc123\n")
	return 0, nil
}
