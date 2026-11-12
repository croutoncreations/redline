package profilelogin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Timeouts per mode. A renewal runs inside the usage monitor's cycle, so
// these bound how long one seat can delay the others.
const (
	StatusTimeout = 30 * time.Second
	PromptTimeout = 90 * time.Second
)

// ClaudeRenewer runs the claude binary under a profile. It passes no
// credential and reads none: Claude Code finds its own login through
// CLAUDE_CONFIG_DIR.
type ClaudeRenewer struct {
	// Binary is the claude executable; empty looks up "claude" on PATH.
	Binary string
	// WorkDir is an empty directory only this user can write; empty uses
	// a "redline-profile-login" directory under the user's temp folder.
	WorkDir string
	// timeout replaces the per-mode timeout; for tests.
	timeout time.Duration
}

// Args for each mode. The prompt is the smallest real request: haiku, no
// tools, no MCP servers, no skills or settings, nothing saved, one word back.
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

// inherited is what a renewal keeps from the service's environment. Only
// what Claude Code needs to find its home, keychain and network: nothing
// that could sign it in as someone else, send the login to another host,
// remap the model, or leak another tool's secret.
var inherited = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "PATH": true, "TMPDIR": true, "SHELL": true,
	"LANG": true, "TZ": true,
	// A proxy the person set up for their own network.
	"HTTPS_PROXY": true, "https_proxy": true, "HTTP_PROXY": true, "http_proxy": true,
	"NO_PROXY": true, "no_proxy": true,
}

// ProfileEnvironment is env for a Claude Code process renewing the profile
// at dir: an allowlist of the service's environment plus the profile.
func ProfileEnvironment(env []string, dir string) []string {
	kept := make([]string, 0, len(inherited)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if inherited[key] || strings.HasPrefix(key, "LC_") {
			kept = append(kept, entry)
		}
	}
	// A command-capture shim in front of claude (Gatepost) must not route
	// an unattended credential renewal through its intercepting proxy.
	return append(kept, "GATEPOST_HOST_DISABLE=1", "CLAUDE_CONFIG_DIR="+dir)
}

func (r ClaudeRenewer) workDir() (string, error) {
	dir := r.WorkDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "redline-profile-login")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("profile login work directory %s must be a private directory", dir)
	}
	return dir, nil
}

func (r ClaudeRenewer) Renew(ctx context.Context, dir string, mode Mode) error {
	binary := r.Binary
	if binary == "" {
		if testing.Testing() {
			// No test may start the real Claude Code on a developer's Mac.
			return errors.New("refusing to run the real claude binary under go test")
		}
		found, err := exec.LookPath("claude")
		if err != nil {
			return fmt.Errorf("claude is not on the service's PATH")
		}
		binary = found
	}
	work, err := r.workDir()
	if err != nil {
		return err
	}
	timeout := PromptTimeout
	if mode == ModeStatus {
		timeout = StatusTimeout
	}
	if r.timeout > 0 {
		timeout = r.timeout
	}
	// A service shutdown must not interrupt Claude Code between receiving
	// a new refresh token and saving it: only the timeout stops a run, and
	// it asks politely first.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, renewArgs(mode)...)
	cmd.Env = append(ProfileEnvironment(os.Environ(), dir), "PWD="+work)
	cmd.Dir = work
	// The whole process group is stopped, so nothing it started keeps
	// running.
	ownProcessGroup(cmd)
	// After the cancel, give it a moment, then stop waiting for any
	// descendant still holding the output open.
	cmd.WaitDelay = 5 * time.Second
	// Output is discarded: auth status prints the account's email and
	// organisation, which must not reach the service log.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("claude %s timed out after %s", mode, timeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("claude %s exited with status %d", mode, exit.ExitCode())
		}
		return fmt.Errorf("claude %s could not start", mode)
	}
	return nil
}
