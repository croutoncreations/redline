package domain_test

import (
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
)

func TestBaseBranchNameNeverYieldsDoubleOriginPrefix(t *testing.T) {
	for input, want := range map[string]string{
		"main": "main", "origin/main": "main", "refs/remotes/origin/main": "main",
		"refs/heads/main": "main", " origin/release/1.2 ": "release/1.2", "": "",
	} {
		if got := domain.BaseBranchName(input); got != want {
			t.Errorf("BaseBranchName(%q) = %q, want %q", input, got, want)
		}
	}
}
