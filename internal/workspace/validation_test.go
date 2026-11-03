package workspace_test

import (
	"context"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/workspace"
	"testing"
)

func TestLocalValidationLeavesManagedAndCustomPathSemanticsToTheirProvider(t *testing.T) {
	for _, profile := range []domain.ExecutionProfile{
		{WorkspaceProvider: "git-worktree", AgentContextID: "remote-runtime", Repository: "/remote/repository"},
		{WorkspaceProvider: "devx", Repository: "/managed/repository"},
		{WorkspaceProvider: "command", Repository: "/custom/repository"},
		{WorkspaceProvider: "runtime-owned", Repository: "/runtime/repository"},
	} {
		if err := workspace.ValidateLocalRepository(context.Background(), profile); err != nil {
			t.Fatalf("provider %s should own validation: %v", profile.WorkspaceProvider, err)
		}
	}
}
