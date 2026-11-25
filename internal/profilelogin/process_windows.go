//go:build windows

package profilelogin

import (
	"os"
	"os/exec"
)

// ownProcessGroup keeps exec's default on Windows: the timeout kills the
// process, and WaitDelay stops waiting for anything it left running.
func ownProcessGroup(*exec.Cmd) {}

// ownedByMe is not checked on Windows, where the keep-alive is off.
func ownedByMe(os.FileInfo) bool { return true }

// killGroup is a no-op on Windows, where the keep-alive is off.
func killGroup(*exec.Cmd) {}
