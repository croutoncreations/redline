package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecWaitDelay bounds how long Run waits, after the context is cancelled or
// the command exits, for the command's output pipes to close. Without it a
// killed command whose child still holds stdout or stderr (git spawning ssh
// or a credential helper, a shell spawning a background job) blocks Run until
// that child exits.
const ExecWaitDelay = 5 * time.Second

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (int, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.WaitDelay = ExecWaitDelay
	var environment []string
	if command.Env != nil {
		environment = append(make([]string, 0, len(command.Env)), command.Env...)
	} else {
		environment = os.Environ()
	}
	if command.Dir != "" {
		filtered := environment[:0]
		for _, variable := range environment {
			if !strings.HasPrefix(variable, "PWD=") {
				filtered = append(filtered, variable)
			}
		}
		cmd.Env = append(filtered, "PWD="+command.Dir)
	} else {
		cmd.Env = environment
	}
	cmd.Stdin = command.Stdin
	cmd.Stdout = command.Stdout
	cmd.Stderr = command.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil {
		// The command itself exited; only a leftover child kept the pipes
		// open. Report the command's own result.
		return cmd.ProcessState.ExitCode(), nil
	}
	return -1, err
}
