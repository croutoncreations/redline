//go:build !windows

package profilelogin

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ownProcessGroup starts cmd in its own process group and makes a timeout
// send SIGTERM to the whole group, then SIGKILL to whatever is left.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		group := -cmd.Process.Pid
		err := syscall.Kill(group, syscall.SIGTERM)
		time.AfterFunc(groupKillDelay, func() { _ = syscall.Kill(group, syscall.SIGKILL) })
		return err
	}
}

// groupKillDelay is how long a timed-out group gets to exit on SIGTERM.
const groupKillDelay = 3 * time.Second

// killGroup stops anything a finished run left behind in its group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// ownedByMe: another user must not be able to pre-create the work
// directory under a shared /tmp.
func ownedByMe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
