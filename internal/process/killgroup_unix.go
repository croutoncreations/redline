//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// configureKillGroup starts the command as its own process group leader and
// kills the whole group on cancellation, so grandchildren die too.
func configureKillGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
