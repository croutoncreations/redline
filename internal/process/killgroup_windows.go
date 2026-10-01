//go:build windows

package process

import "os/exec"

// configureKillGroup is a no-op on Windows: process groups work differently
// there, and the default cancel kills the direct child.
func configureKillGroup(*exec.Cmd) {}
