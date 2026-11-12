//go:build !windows

package profilelogin

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd in its own process group and makes a timeout
// send SIGTERM to the whole group.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}
