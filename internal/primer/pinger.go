package primer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/croutoncreations/redline/internal/domain"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

// Pinger sends the priming message.
type Pinger interface {
	Ping(ctx context.Context, settings domain.PrimerSettings) (string, error)
}

// scrubbedVariables would route the ping to API billing or another cloud,
// which neither opens the subscription window nor should cost money.
// Endpoint overrides and custom headers are removed too: without its API
// key, a proxy would otherwise receive the subscription-authenticated request.
var scrubbedVariables = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS",
}

// scrubbedPrefixes and scrubbedSuffixes catch the whole family of cloud
// switches (CLAUDE_CODE_USE_*) and endpoint overrides (ANTHROPIC_*BASE_URL).
var (
	scrubbedPrefixes = []string{"CLAUDE_CODE_USE_"}
	scrubbedSuffixes = []string{"_BASE_URL"}
)

// outputLimit bounds what is kept from the CLI's output; the rest is drained.
const outputLimit = 16 << 10

const pingTimeout = 2 * time.Minute

// ClaudePinger sends one message through the local Claude Code login.
type ClaudePinger struct {
	Runner  redprocess.Runner
	Command string
	Environ func() []string
}

func (p ClaudePinger) Ping(ctx context.Context, settings domain.PrimerSettings) (string, error) {
	directory, err := os.MkdirTemp("", "redline-primer-")
	if err != nil {
		return "", fmt.Errorf("create ping directory: %w", err)
	}
	defer os.RemoveAll(directory)
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	output, stderr, err := p.run(ctx, directory, settings)
	if err != nil && strings.Contains(strings.ToLower(stderr), "unknown option") {
		// Without --safe-mode the ping would load user hooks, plugins, and
		// settings that may carry API credentials; fail closed instead.
		return "", fmt.Errorf("this Claude Code is too old for the window primer (it needs --safe-mode); update Claude Code: %s", tail(stderr, 200))
	}
	if err != nil {
		detail := firstNonEmpty(tail(stderr, 400), tail(output, 400))
		if detail != "" {
			return tail(output, 500), fmt.Errorf("%w: %s", err, detail)
		}
		return tail(output, 500), err
	}
	return tail(output, 500), nil
}

func (p ClaudePinger) run(ctx context.Context, directory string, settings domain.PrimerSettings) (string, string, error) {
	// The model rides in one argument so it can never be read as a flag.
	args := []string{"--print", "--model=" + settings.Model, "--no-session-persistence", "--safe-mode",
		"--strict-mcp-config", "--tools", "", "--output-format", "text"}
	var stdout, stderr boundedBuffer
	command := redprocess.Command{
		Name: firstNonEmpty(p.Command, "claude"), Args: args, Dir: directory,
		Env: ScrubEnvironment(p.environ()), Stdin: strings.NewReader(settings.Prompt),
		Stdout: &stdout, Stderr: &stderr,
		// A ping that will not die must not stall the primer: kill its whole
		// process group and stop waiting on its pipes shortly after.
		KillGroup: true, WaitDelay: 5 * time.Second,
	}
	runner := p.Runner
	if runner == nil {
		runner = redprocess.ExecRunner{}
	}
	exitCode, err := runner.Run(ctx, command)
	if err == nil && exitCode != 0 {
		err = fmt.Errorf("claude exited with code %d", exitCode)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("claude did not answer within %s", pingTimeout)
	} else if ctx.Err() != nil {
		err = fmt.Errorf("ping cancelled: %w", ctx.Err())
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func (p ClaudePinger) environ() []string {
	if p.Environ != nil {
		return p.Environ()
	}
	return os.Environ()
}

// ScrubEnvironment removes variables that would bill the ping to an API
// account instead of the subscription window.
func ScrubEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, variable := range environment {
		name, _, _ := strings.Cut(variable, "=")
		if !scrubbed(strings.ToUpper(name)) {
			result = append(result, variable)
		}
	}
	return result
}

func scrubbed(name string) bool {
	for _, exact := range scrubbedVariables {
		if name == exact {
			return true
		}
	}
	for _, prefix := range scrubbedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for _, suffix := range scrubbedSuffixes {
		if strings.HasPrefix(name, "ANTHROPIC_") && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// boundedBuffer keeps the last outputLimit bytes written and discards the
// rest, so a misbehaving CLI cannot grow service memory without bound.
type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if extra := len(b.data) - outputLimit; extra > 0 {
		b.data = append(b.data[:0], b.data[extra:]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

func tail(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return "…" + value[start:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
