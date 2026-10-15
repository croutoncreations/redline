package process_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	redprocess "github.com/croutoncreations/redline/internal/process"
)

func TestExecRunnerAlignsPWDWithCommandDirectory(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/usr/bin/env", Dir: directory, Env: []string{"PATH=/usr/bin:/bin", "PWD=/wrong/directory"},
		Stdout: &output,
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("exit_code=%d err=%v", exitCode, err)
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if line == "PWD="+directory {
			return
		}
	}
	t.Fatalf("environment did not contain corrected PWD: %q", output.String())
}

func TestExecRunnerHonorsExplicitEmptyEnvironment(t *testing.T) {
	var output bytes.Buffer
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/usr/bin/env", Env: []string{},
		Stdout: &output,
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("exit_code=%d err=%v", exitCode, err)
	}
	if output.Len() != 0 {
		t.Fatalf("expected empty environment, got: %q", output.String())
	}
}

// A cancelled command whose background child still holds the output pipe
// must not block Run until that child exits.
func TestExecRunnerReturnsAfterCancelWhenChildHoldsPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	started := time.Now()
	_, _ = (redprocess.ExecRunner{}).Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-c", "sleep 60 & sleep 60"}, Stdout: &output,
	})
	if elapsed := time.Since(started); elapsed > redprocess.ExecWaitDelay+5*time.Second {
		t.Fatalf("Run blocked for %s after cancellation", elapsed)
	}
}

// A command that exits normally but leaves a child holding stdout reports
// its own exit code once the wait delay closes the pipes.
func TestExecRunnerReportsExitCodeWhenChildOutlivesCommand(t *testing.T) {
	var output bytes.Buffer
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/bin/sh", Args: []string{"-c", "sleep 60 & echo done"}, Stdout: &output,
	})
	if err != nil || exitCode != 0 || !strings.Contains(output.String(), "done") {
		t.Fatalf("exit_code=%d err=%v output=%q", exitCode, err, output.String())
	}
}
