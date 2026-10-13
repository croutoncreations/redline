//go:build !windows

package process_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	redprocess "github.com/croutoncreations/redline/internal/process"
)

func TestExecRunnerKillGroupKillsGrandchildrenOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	var output bytes.Buffer
	start := time.Now()
	// The shell leaves a sleeping grandchild holding stdout, then blocks
	// itself. Without WaitDelay only the group kill can end the pipes.
	_, _ = (redprocess.ExecRunner{}).Run(ctx, redprocess.Command{
		Name: "/bin/sh", Args: []string{"-c", "sleep 30 & echo $! > " + pidFile + "; sleep 30"}, Stdout: &output,
		KillGroup: true,
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v; the process group was not killed", elapsed)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived the group kill", pid)
	}
}

func TestExecRunnerTreatsALingeringGrandchildAfterSuccessAsSuccess(t *testing.T) {
	var output bytes.Buffer
	// The shell exits 0 at once; a grandchild keeps stdout open briefly.
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/bin/sh", Args: []string{"-c", "sleep 1 & echo done"}, Stdout: &output,
		KillGroup: true, WaitDelay: 100 * time.Millisecond,
	})
	if err != nil || exitCode != 0 || !strings.Contains(output.String(), "done") {
		t.Fatalf("exit_code=%d err=%v output=%q; a successful command must not fail on WaitDelay", exitCode, err, output.String())
	}
}
