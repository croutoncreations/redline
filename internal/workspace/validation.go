package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/domain"
)

// ValidateLocalRepository checks only the built-in local worktree provider.
// Remote runtimes and custom/DevX workspace providers own their path semantics.
func ValidateLocalRepository(ctx context.Context, profile domain.ExecutionProfile) error {
	if profile.WorkspaceProvider != "git-worktree" || profile.AgentContextID != "" {
		return nil
	}
	info, err := os.Stat(profile.Repository)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repository must be an existing directory on this Redline host; choose a local Git repository: %s", profile.Repository)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", profile.Repository, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("repository must be a Git working tree; choose the directory containing your checkout: %s", profile.Repository)
	}
	ref := profile.BaseBranch
	if ref == "" {
		ref = "HEAD"
	}
	if err := exec.CommandContext(ctx, "git", "-C", profile.Repository, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Run(); err != nil {
		return fmt.Errorf("base branch must be an existing commit in the repository; check %q or create the repository's first commit", ref)
	}
	return nil
}
