package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (int, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
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
	return -1, err
}
