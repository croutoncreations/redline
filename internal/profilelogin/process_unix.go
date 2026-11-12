//go:build !windows

package profilelogin

import (
	"os"
	"os/exec"
	"syscall"
)

// ownProcessGroup starts cmd in its own process group and makes a timeout
// send SIGTERM to the whole group.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// ownedByMe: another user must not be able to pre-create the work
// directory under a shared /tmp.
func ownedByMe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
