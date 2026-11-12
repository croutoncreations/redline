//go:build !windows

package profilelogin

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A descendant that ignores SIGTERM does not outlive the renewal.
func TestADescendantIgnoringSIGTERMIsKilled(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	binary := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n(trap '' TERM; echo $$ > " + pidFile + "; exec sleep 600) &\nsleep 1\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (ClaudeRenewer{Binary: binary}).Renew(context.Background(), "/p", ModeStatus); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("descendant %d survived the renewal", pid)
}
