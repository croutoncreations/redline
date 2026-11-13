package cursorusage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
)

func accounts(app, cli string) *LocalAccountCheck {
	return &LocalAccountCheck{
		AppAccount: func(context.Context) (string, error) { return app, nil },
		CLIAccount: func(context.Context) (string, error) { return cli, nil },
		TTL:        5 * time.Minute,
	}
}

func TestAccountCheckMatchesIgnoringCaseAndSpace(t *testing.T) {
	if err := accounts("Person+cursor@Example.com", " person+cursor@example.com\n").Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Every failure fails closed with the account_mismatch reason and never
// names either address.
func TestAccountCheckFailsClosed(t *testing.T) {
	for name, check := range map[string]*LocalAccountCheck{
		"different":      accounts("a@example.com", "b@example.com"),
		"cli signed out": accounts("a@example.com", ""),
		"app signed out": accounts("", "a@example.com"),
		"cli error": {
			AppAccount: func(context.Context) (string, error) { return "a@example.com", nil },
			CLIAccount: func(context.Context) (string, error) { return "", errors.New("boom") },
		},
	} {
		err := check.Verify(context.Background())
		var reasoned interface{ Reason() string }
		if err == nil || !errors.As(err, &reasoned) || reasoned.Reason() != ReasonAccountMismatch {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "example.com") {
			t.Errorf("%s: address leaked: %v", name, err)
		}
	}
}

// A confirmed match is reused for a while, so a five-minute poll does not run
// the CLI every time; a mismatch is not cached, so signing in again takes
// effect on the next fetch.
func TestAccountCheckCachesOnlySuccess(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	calls, cli := 0, "a@example.com"
	check := &LocalAccountCheck{
		AppAccount: func(context.Context) (string, error) { return "a@example.com", nil },
		CLIAccount: func(context.Context) (string, error) { calls++; return cli, nil },
		TTL:        5 * time.Minute,
		Now:        func() time.Time { return now },
	}
	_ = check.Verify(context.Background())
	_ = check.Verify(context.Background())
	if calls != 1 {
		t.Fatalf("cli asked %d times within the TTL", calls)
	}
	now = now.Add(6 * time.Minute)
	cli = "b@example.com"
	if check.Verify(context.Background()) == nil {
		t.Fatal("mismatch after the TTL was not detected")
	}
	cli = "a@example.com"
	if check.Verify(context.Background()) != nil || calls != 3 {
		t.Fatalf("recovery not immediate: calls=%d", calls)
	}
}

// The CLI's real status shape: pretty-printed, after progress text.
func TestParseCLIAccountReadsPrettyPrintedStatus(t *testing.T) {
	email, err := parseCLIAccount([]byte("Checking login...\n{\n  \"isAuthenticated\": true,\n  \"userInfo\": {\n    \"email\": \"a@example.com\"\n  }\n}\n"))
	if err != nil || email != "a@example.com" {
		t.Fatalf("email=%q err=%v", email, err)
	}
	if email, _ := parseCLIAccount([]byte(`{"isAuthenticated":false}`)); email != "" {
		t.Fatalf("signed out returned %q", email)
	}
}

// A mismatch stops the usage source before any usage is read.
func TestUsageSourceRefusesAnUnconfirmedAccount(t *testing.T) {
	fetched := false
	source := UsageSource{SourceName: "openusage", Accounts: accounts("a@example.com", "b@example.com"),
		Collector: func(config.Provider) Fetcher { fetched = true; return fixedFetcher{} }}
	_, _, err := source.Fetch(context.Background(), config.Provider{Provider: "cursor"})
	if err == nil || fetched {
		t.Fatalf("err=%v fetched=%v", err, fetched)
	}
}
