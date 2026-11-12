//go:build windows

package profilelogin

import "os/exec"

// ownProcessGroup keeps exec's default on Windows: the timeout kills the
// process, and WaitDelay stops waiting for anything it left running.
func ownProcessGroup(*exec.Cmd) {}
