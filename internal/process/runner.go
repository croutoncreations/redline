package process

import (
	"context"
	"io"
	"time"
)

type Command struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// KillGroup runs the command in its own process group and, when ctx
	// ends, kills the whole group. Without it only the direct child is
	// killed; a grandchild still holding the output pipes keeps Run from
	// returning. Not supported on Windows, where only the child is killed.
	KillGroup bool
	// WaitDelay bounds how long Run waits for the output pipes to close
	// after ctx ends. Zero waits indefinitely.
	WaitDelay time.Duration
}

type Runner interface {
	Run(context.Context, Command) (int, error)
}
