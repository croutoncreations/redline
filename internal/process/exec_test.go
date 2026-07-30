package process_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	redprocess "github.com/jfox/redline/internal/process"
)

func TestExecRunnerAlignsPWDWithCommandDirectory(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/usr/bin/env", Dir: directory, Env: []string{"PATH=/usr/bin:/bin", "PWD=/wrong/directory"},
		Stdout: &output,
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("exit_code=%d err=%v", exitCode, err)
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if line == "PWD="+directory {
			return
		}
	}
	t.Fatalf("environment did not contain corrected PWD: %q", output.String())
}

func TestExecRunnerHonorsExplicitEmptyEnvironment(t *testing.T) {
	var output bytes.Buffer
	exitCode, err := (redprocess.ExecRunner{}).Run(context.Background(), redprocess.Command{
		Name: "/usr/bin/env", Env: []string{},
		Stdout: &output,
	})
	if err != nil || exitCode != 0 {
		t.Fatalf("exit_code=%d err=%v", exitCode, err)
	}
	if output.Len() != 0 {
		t.Fatalf("expected empty environment, got: %q", output.String())
	}
}
