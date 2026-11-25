package cursorusage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/nativeusage"
	redprocess "github.com/croutoncreations/redline/internal/process"
)

// ReasonAccountMismatch marks a Cursor account whose jobs would run as a
// different Cursor user than the one whose usage Redline reads.
const ReasonAccountMismatch = "account_mismatch"

// AccountCheck confirms that the Cursor CLI, which runs jobs, is signed in to
// the same Cursor account whose usage Redline schedules against.
//
// Usage comes from the Cursor app's session (directly, or through OpenUsage,
// which reads the same session). Jobs run as whatever `agent login` holds. If
// the two differ, Redline would admit work against one account's headroom and
// bill it to another, so a Cursor account that cannot be confirmed is not
// scheduled.
type AccountCheck interface {
	Verify(ctx context.Context) error
}

// accountMismatch is the error a failed check returns. It carries a reason
// the dashboard names, and never either account's address.
type accountMismatch struct{ message string }

func (e accountMismatch) Error() string { return e.message }
func (accountMismatch) Reason() string  { return ReasonAccountMismatch }

var (
	errAccountsDiffer = accountMismatch{"the Cursor CLI and the Cursor app are signed in to different accounts; " +
		"sign both in to the account whose usage Redline should schedule against (`agent login`)"}
	errCLIAccountUnknown = accountMismatch{"cannot confirm which Cursor account the CLI is signed in to; " +
		"run `agent status` and sign in with `agent login`"}
	errAppAccountUnknown = accountMismatch{"cannot confirm which Cursor account the Cursor app is signed in to; " +
		"open Cursor and sign in"}
)

// LocalAccountCheck compares the Cursor app's signed-in account with the
// Cursor CLI's. Only digests of the normalized addresses are compared and
// kept; neither address is logged, stored or returned.
type LocalAccountCheck struct {
	// AppAccount returns the Cursor app's signed-in email, or "" when none.
	AppAccount func(context.Context) (string, error)
	// CLIAccount returns the Cursor CLI's signed-in email, or "" when none.
	CLIAccount func(context.Context) (string, error)
	// TTL is how long a successful comparison is reused. Failures are not
	// cached, so signing in again takes effect on the next fetch.
	TTL time.Duration
	Now func() time.Time

	mu        sync.Mutex
	confirmed time.Time
}

// NewLocalAccountCheck reads the Cursor app's state database (read-only, the
// cached email only) and asks `cursor-agent status` which account it holds.
func NewLocalAccountCheck(now func() time.Time) *LocalAccountCheck {
	var state nativeusage.CursorStateReader
	if configDir, err := os.UserConfigDir(); err == nil {
		state = nativeusage.CursorStateDB{Path: filepath.Join(configDir, "Cursor", "User", "globalStorage", "state.vscdb")}
	}
	return &LocalAccountCheck{
		AppAccount: func(ctx context.Context) (string, error) {
			if state == nil {
				return "", nil
			}
			return state.Value(ctx, "cursorAuth/cachedEmail")
		},
		CLIAccount: cursorCLIAccount,
		TTL:        5 * time.Minute,
		Now:        now,
	}
}

func (c *LocalAccountCheck) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *LocalAccountCheck) Verify(ctx context.Context) error {
	c.mu.Lock()
	if !c.confirmed.IsZero() && c.now().Sub(c.confirmed) < c.TTL {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	app, err := c.AppAccount(ctx)
	if err != nil || accountDigest(app) == "" {
		return errAppAccountUnknown
	}
	cli, err := c.CLIAccount(ctx)
	if err != nil || accountDigest(cli) == "" {
		return errCLIAccountUnknown
	}
	if accountDigest(app) != accountDigest(cli) {
		return errAccountsDiffer
	}
	c.mu.Lock()
	c.confirmed = c.now()
	c.mu.Unlock()
	return nil
}

// accountDigest normalizes an address and hashes it, so the comparison never
// needs to hold or print the address itself.
func accountDigest(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(email))
	return string(sum[:])
}

// cursorCLIAccount runs `cursor-agent status --format json` with the same
// environment Redline's Cursor runs get, so a CURSOR_API_KEY elsewhere cannot
// make the check describe an account the runs would not use.
func cursorCLIAccount(ctx context.Context) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "cursor-agent", "status", "--format", "json")
	command.Env = redprocess.WithoutCursorCredentials(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", errors.New("cursor-agent status failed")
	}
	return parseCLIAccount(output)
}

// parseCLIAccount reads the email from `status --format json`, which the CLI
// pretty-prints across lines, possibly after progress text.
func parseCLIAccount(output []byte) (string, error) {
	text := string(output)
	if start := strings.Index(text, "{"); start >= 0 {
		text = text[start:]
	}
	var status struct {
		IsAuthenticated bool `json:"isAuthenticated"`
		UserInfo        struct {
			Email string `json:"email"`
		} `json:"userInfo"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &status); err != nil {
		return "", errors.New("cursor-agent status was not JSON")
	}
	if !status.IsAuthenticated {
		return "", nil
	}
	return status.UserInfo.Email, nil
}
