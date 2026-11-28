package apicredits

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
)

type memorySecrets struct {
	mu    sync.Mutex
	items map[string]string
}

func (m *memorySecrets) Load(_ context.Context, account, kind string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value, ok := m.items[account+"/"+kind]; ok {
		return value, nil
	}
	return "", ErrSecretNotFound
}
func (m *memorySecrets) Replace(_ context.Context, account, kind, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.items == nil {
		m.items = map[string]string{}
	}
	m.items[account+"/"+kind] = value
	return nil
}
func (m *memorySecrets) Clear(_ context.Context, account, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, account+"/"+kind)
	return nil
}

type nextSource struct{ calls int }

func (n *nextSource) Name() string { return "native" }
func (n *nextSource) Fetch(context.Context, config.Provider) (decision.UsageSnapshot, []byte, error) {
	n.calls++
	return decision.UsageSnapshot{Provider: "codex"}, nil, nil
}

func creditsProvider() config.Provider {
	return config.Provider{Provider: config.ProviderAPICredits, Label: "Credits",
		APICredits: &config.APICredits{OrganizationID: "org-test"}}
}

func TestUsageSourcePassesOtherProvidersThrough(t *testing.T) {
	next := &nextSource{}
	source := UsageSource{Next: next}
	if _, _, err := source.Fetch(context.Background(), config.Provider{Provider: "codex"}); err != nil || next.calls != 1 {
		t.Fatalf("err = %v calls = %d", err, next.calls)
	}
	if source.Name() != "native" {
		t.Fatalf("name = %s", source.Name())
	}
}

func TestUsageSourceReadsCreditsWithTheAccountsSessionKey(t *testing.T) {
	server, seen := fakePlatform(t, http.StatusOK, string(loadFixture(t)))
	secrets := &memorySecrets{}
	_ = secrets.Replace(context.Background(), "api-credits", SecretSessionKey, fakeSessionKey)
	source := UsageSource{Next: &nextSource{}, Client: testClient(server), Secrets: secrets}
	provider := creditsProvider()
	ctx := WithAccount(context.Background(), "api-credits")
	snapshot, raw, err := source.Fetch(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != "native" || snapshot.Credits == nil || snapshot.Credits.PromoRemaining != 110 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if strings.Contains(string(raw), "sess-") || strings.Contains(string(raw), "chatgpt_api_credits_promo_") {
		t.Fatal("raw payload leaks a credential or grant id")
	}
	if (*seen)[0].Header.Get("OpenAI-Organization") != "org-test" {
		t.Fatal("organization not sent")
	}
}

// A missing or expired session key is a sign-in problem the dashboard names,
// not a generic error.
func TestUsageSourceReportsMissingOrExpiredSessionAsSignedOut(t *testing.T) {
	server, _ := fakePlatform(t, http.StatusUnauthorized, `{}`)
	secrets := &memorySecrets{}
	source := UsageSource{Next: &nextSource{}, Client: testClient(server), Secrets: secrets}
	ctx := WithAccount(context.Background(), "api-credits")
	for name, setup := range map[string]func(){
		"missing": func() {},
		"expired": func() { _ = secrets.Replace(ctx, "api-credits", SecretSessionKey, fakeSessionKey) },
	} {
		setup()
		_, _, err := source.Fetch(ctx, creditsProvider())
		var reasoned interface{ Reason() string }
		if !errors.As(err, &reasoned) || reasoned.Reason() != "signed_out" {
			t.Fatalf("%s: err = %v", name, err)
		}
		if strings.Contains(err.Error(), "sess-") {
			t.Fatalf("%s: error leaks the key", name)
		}
	}
}

// Without the account ID the source cannot know whose key to use, so it
// refuses rather than guessing.
func TestUsageSourceNeedsTheAccountID(t *testing.T) {
	source := UsageSource{Next: &nextSource{}, Secrets: &memorySecrets{}}
	if _, _, err := source.Fetch(context.Background(), creditsProvider()); err == nil {
		t.Fatal("fetched without an account id")
	}
}
