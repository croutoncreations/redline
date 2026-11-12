package profilelogin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RenewTimeout bounds one Claude Code run. auth status takes seconds; the
// prompt a few more.
const RenewTimeout = 90 * time.Second

// ClaudeRenewer runs the claude binary under a profile. It passes no
// credential and reads none: Claude Code finds its own login through
// CLAUDE_CONFIG_DIR.
type ClaudeRenewer struct {
	// Binary is the claude executable; empty looks up "claude" on PATH.
	Binary string
	// Command builds the process; nil uses exec.CommandContext. Tests
	// replace it.
	Command func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// Args for each mode. The prompt is the smallest real request: haiku, no
// tools, no MCP servers, no skills, nothing saved, one word back.
func renewArgs(mode Mode) []string {
	switch mode {
	case ModeStatus:
		return []string{"auth", "status", "--json"}
	default:
		return []string{"--print", "--model", "haiku", "--tools", "", "--strict-mcp-config",
			"--disable-slash-commands", "--no-session-persistence", "--setting-sources", "",
			"Reply with the single word ok."}
	}
}

// ProfileEnvironment is env for a Claude Code process on the profile at dir:
// any inherited CLAUDE_CONFIG_DIR is replaced, and variables that would sign
// Claude Code in as someone else, or send the profile's token elsewhere, are
// dropped.
func ProfileEnvironment(env []string, dir string) []string {
	drop := map[string]bool{
		"CLAUDE_CONFIG_DIR": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "ANTHROPIC_API_KEY": true,
		"ANTHROPIC_AUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true, "CLAUDE_CODE_USE_BEDROCK": true,
		"CLAUDE_CODE_USE_VERTEX": true,
	}
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if key, _, _ := strings.Cut(entry, "="); !drop[key] {
			kept = append(kept, entry)
		}
	}
	return append(kept, "CLAUDE_CONFIG_DIR="+dir)
}

func (r ClaudeRenewer) Renew(ctx context.Context, dir string, mode Mode) error {
	binary := r.Binary
	if binary == "" {
		found, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude is not on PATH: %w", err)
		}
		binary = found
	}
	ctx, cancel := context.WithTimeout(ctx, RenewTimeout)
	defer cancel()
	command := r.Command
	if command == nil {
		command = exec.CommandContext
	}
	cmd := command(ctx, binary, renewArgs(mode)...)
	cmd.Env = ProfileEnvironment(os.Environ(), dir)
	cmd.Dir = os.TempDir()
	cmd.Stdin = nil
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(output.String())
		if len(text) > 200 {
			text = text[:200]
		}
		return fmt.Errorf("claude %s: %w: %s", mode, err, text)
	}
	return nil
}
