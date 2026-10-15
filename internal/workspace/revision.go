package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

type RevisionResolver interface {
	Resolve(context.Context, domain.ExecutionProfile) (string, error)
}

type GitRevisionResolver struct{ Runner redprocess.Runner }

// Resolve reports the revision a new run of this profile would start from.
//
// Git worktree and DevX runs start from the freshly fetched origin/<base>, so
// their revision comes from SyncBase too: a stale local branch must not read
// as "unchanged" and keep a repo-change-gated job from ever reaching the
// fetch. The scheduler calls this for every candidate, so the fetch result is
// reused for a short TTL (see CachedSyncBase); Prepare fetches fresh. When
// origin is present but cannot be fetched the revision is unknown
// and an error is returned, which the scheduler reports as a skip reason.
//
// Other providers run in the checkout as it is, so their revision is the
// local base, which keeps unpushed local commits counting as changes.
func (r GitRevisionResolver) Resolve(ctx context.Context, profile domain.ExecutionProfile) (string, error) {
	if profile.Repository == "" {
		return "", fmt.Errorf("repository is required to resolve source revision")
	}
	if profile.WorkspaceProvider == "git-worktree" || profile.WorkspaceProvider == "devx" {
		synced, err := CachedSyncBase(ctx, r.runner(), profile.Repository, profile.BaseBranch)
		if err != nil {
			return "", fmt.Errorf("resolve repository revision: %w", err)
		}
		return synced.SHA, nil
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
