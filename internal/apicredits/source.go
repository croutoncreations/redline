package apicredits

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

type accountKey struct{}

// WithAccount records which Redline account a usage fetch is for. The
// usage.Source contract passes only the provider configuration, and a
// credits account's session key is stored under its account ID.
func WithAccount(ctx context.Context, account string) context.Context {
	return context.WithValue(ctx, accountKey{}, account)
}

// AccountFrom returns the account WithAccount recorded, or "".
func AccountFrom(ctx context.Context) string {
	account, _ := ctx.Value(accountKey{}).(string)
	return account
}

// UsageSource is a usage.Source for API credits accounts of either vendor.
// It wraps the native source and handles only those providers, so adding a
// credits account changes nothing for any other.
type UsageSource struct {
	Next interface {
		Name() string
		Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error)
	}
	// Client reads openai-api-credits accounts; Console reads
	// anthropic-api-credits accounts.
	Client  Client
	Console ConsoleClient
	Secrets SecretStore
}

func (s UsageSource) Name() string { return s.Next.Name() }

func (s UsageSource) Fetch(ctx context.Context, provider config.Provider) (decision.UsageSnapshot, []byte, error) {
	if !provider.IsAPICredits() {
		return s.Next.Fetch(ctx, provider)
	}
	account := AccountFrom(ctx)
	if account == "" {
		return decision.UsageSnapshot{}, nil, errors.New("API credits fetch is missing its account")
	}
	settings := provider.EffectiveAPICredits()
	vendor := provider.APICreditsVendor()
	key, err := ResolveSecret(ctx, s.Secrets, vendor, account, SecretSessionKey, settings.SessionKeyFile)
	if errors.Is(err, ErrSecretNotFound) {
		what := "OpenAI platform"
		if vendor == config.APICreditsVendorAnthropic {
			what = "Claude Console"
		}
		return decision.UsageSnapshot{}, nil, signedOut{fmt.Errorf("no %s session key is saved; run `redline account set-session-key %s`", what, account)}
	}
	if err != nil {
		return decision.UsageSnapshot{}, nil, err
	}
	credential := Credential{SessionKey: key, OrganizationID: settings.OrganizationID}
	var report FetchedReport
	if vendor == config.APICreditsVendorAnthropic {
		report, err = s.Console.Fetch(ctx, credential)
	} else {
		report, err = s.Client.Fetch(ctx, credential)
	}
	switch {
	case errors.Is(err, ErrSessionExpired), errors.Is(err, ErrNotSessionKey),
		errors.Is(err, ErrConsoleSessionExpired), errors.Is(err, ErrNotConsoleSessionKey):
		return decision.UsageSnapshot{}, nil, signedOut{err}
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrConsoleRateLimited):
		return decision.UsageSnapshot{}, nil, rateLimited{err}
	case err != nil:
		return decision.UsageSnapshot{}, nil, err
	}
	snapshot, err := Snapshot(report, settings)
	if err != nil {
		return decision.UsageSnapshot{}, nil, err
	}
	snapshot.Provider = strings.ToLower(strings.TrimSpace(provider.Provider))
	snapshot.Source = s.Name()
	raw, err := RawPayload(report)
	return snapshot, raw, err
}

// signedOut is a missing, rejected or wrong-kind session key: the dashboard
// shows it as signed out and names the fix.
type signedOut struct{ error }

func (signedOut) Reason() string  { return "signed_out" }
func (e signedOut) Unwrap() error { return e.error }

// rateLimited backs further reads off through the usage manager's lockout.
type rateLimited struct{ error }

func (rateLimited) RateLimited() bool         { return true }
func (rateLimited) RetryAfter() time.Duration { return 15 * time.Minute }
func (e rateLimited) Unwrap() error           { return e.error }
