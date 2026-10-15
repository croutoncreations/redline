package process_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
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

// lockedBuffer is written by exec's copy goroutine and read by the test.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// A cancelled command whose background child still holds the output pipe
// must not block Run until that child exits.
func TestExecRunnerReturnsAfterCancelWhenChildHoldsPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	output := &lockedBuffer{}
	started := time.Now()
	_, _ = (redprocess.ExecRunner{WaitDelay: 200 * time.Millisecond}).Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-c", "sleep 3 & sleep 3"}, Stdout: output,
	})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run blocked for %s after cancellation", elapsed)
	}
}

// A command that exits normally but leaves a child holding stdout reports
// its own exit code once the wait delay closes the pipes.
func TestExecRunnerReportsExitCodeWhenChildOutlivesCommand(t *testing.T) {
	for _, want := range []int{0, 7} {
		output := &lockedBuffer{}
		started := time.Now()
		exitCode, err := (redprocess.ExecRunner{WaitDelay: 200 * time.Millisecond}).Run(context.Background(), redprocess.Command{
			Name: "/bin/sh", Args: []string{"-c", fmt.Sprintf("sleep 3 & echo done; exit %d", want)}, Stdout: output,
		})
		if err != nil || exitCode != want || !strings.Contains(output.String(), "done") {
			t.Fatalf("want %d: exit_code=%d err=%v output=%q", want, exitCode, err, output.String())
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("Run blocked for %s waiting on the child", elapsed)
		}
	}
}

func TestExecRunnerDefaultsWaitDelay(t *testing.T) {
	if redprocess.ExecWaitDelay != 5*time.Second {
		t.Fatalf("ExecWaitDelay = %s", redprocess.ExecWaitDelay)
	}
}
