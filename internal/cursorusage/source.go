package cursorusage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/openusage"
)

// UsageSource is a usage.Source for Cursor provider accounts. It wraps the
// source used for every other provider and handles only provider: cursor, so
// adding a Cursor account changes nothing for Claude or Codex.
type UsageSource struct {
	// Next serves every non-Cursor provider.
	Next interface {
		Name() string
		Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error)
	}
	// Collector returns the Cursor fetcher for this source and provider.
	Collector  func(config.Provider) Fetcher
	SourceName string
	Now        func() time.Time
	// Accounts confirms the CLI that runs jobs is signed in to the account
	// whose usage this reads. Nil skips the check (tests only).
	Accounts AccountCheck
}

func (s UsageSource) Name() string { return s.SourceName }

func (s UsageSource) Fetch(ctx context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	if !strings.EqualFold(strings.TrimSpace(provider.Provider), "cursor") {
		return s.Next.Fetch(ctx, provider)
	}
	// Checked before usage is read: headroom on one account is no reason to
	// run jobs billed to another, so an unconfirmed pair yields no snapshot,
	// and the account is not scheduled until it is fixed.
	if s.Accounts != nil {
		if err := s.Accounts.Verify(ctx); err != nil {
			return decision.UsageSnapshot{}, nil, err
		}
	}
	collector := s.Collector(provider)
	if collector == nil {
		return decision.UsageSnapshot{}, nil, errors.New("cursor usage source is unavailable")
	}
	report, err := collector.Fetch(ctx)
	if err != nil {
		if errors.Is(err, nativeusage.ErrCursorSignedOut) {
			return decision.UsageSnapshot{}, nil, signedOut{err}
		}
		return decision.UsageSnapshot{}, nil, errors.New(sanitizeMessage(err.Error()))
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	snapshot, err := Snapshot(report, now())
	raw, _ := json.Marshal(report)
	if err != nil {
		return decision.UsageSnapshot{}, raw, err
	}
	// Stored under the source the manager records as active ("openusage" or
	// "native"), so the dashboard's latest-snapshot-from-source lookup finds
	// it. The report's own source ("cursor" for the native collector) would
	// leave the dashboard saying nothing has been collected.
	snapshot.Source = s.SourceName
	return snapshot, raw, nil
}

// signedOut reports Cursor's local sign-in as missing, so the dashboard can
// say "signed out" and name the fix.
type signedOut struct{ error }

func (signedOut) Reason() string  { return "signed_out" }
func (e signedOut) Unwrap() error { return e.error }

// NewUsageSources wraps the OpenUsage and native sources so provider: cursor
// accounts read Cursor's monthly pools through the same two collectors the
// display-only tracker uses. The native collector reads Cursor's session
// read-only and never refreshes it.
func NewUsageSources(openUsage, native interface {
	Name() string
	Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error)
}, now func() time.Time, accounts AccountCheck) (UsageSource, UsageSource) {
	// One check serves both sources: they read the same Cursor app session.
	fromOpenUsage := UsageSource{Next: openUsage, SourceName: openUsage.Name(), Now: now, Accounts: accounts,
		Collector: func(provider config.Provider) Fetcher {
			base := strings.TrimSpace(provider.OpenUsageURL)
			if base == "" {
				base = "http://127.0.0.1:6736"
			}
			return OpenUsageClient{Client: openusage.Client{BaseURL: base}}
		}}
	fromNative := UsageSource{Next: native, SourceName: native.Name(), Now: now, Accounts: accounts,
		Collector: func(config.Provider) Fetcher {
			return NativeClient{Credentials: nativeusage.NewCursorCredentials(), Now: now}
		}}
	return fromOpenUsage, fromNative
}
