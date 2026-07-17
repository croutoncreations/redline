package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jfox/redline/internal/domain"
	redprocess "github.com/jfox/redline/internal/process"
)

type RevisionResolver interface {
	Resolve(context.Context, domain.ExecutionProfile) (string, error)
}

type GitRevisionResolver struct{ Runner redprocess.Runner }

func (r GitRevisionResolver) Resolve(ctx context.Context, profile domain.ExecutionProfile) (string, error) {
	if profile.Repository == "" {
		return "", fmt.Errorf("repository is required to resolve source revision")
	}
	ref := profile.BaseBranch
	if ref == "" {
		ref = "HEAD"
	}
	var stdout bytes.Buffer
	exitCode, err := r.runner().Run(ctx, redprocess.Command{
		Name: "git", Args: []string{"-C", profile.Repository, "rev-parse", ref},
		Stdout: &stdout, Stderr: io.Discard,
	})
	if err != nil {
		return "", fmt.Errorf("resolve repository revision: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("git rev-parse exited with code %d", exitCode)
	}
	revision := strings.TrimSpace(stdout.String())
	if revision == "" {
		return "", fmt.Errorf("git rev-parse returned an empty revision")
	}
	return revision, nil
}

func (r GitRevisionResolver) runner() redprocess.Runner {
	if r.Runner != nil {
		return r.Runner
	}
	return redprocess.ExecRunner{}
}
