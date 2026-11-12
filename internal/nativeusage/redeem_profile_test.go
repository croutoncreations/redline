package nativeusage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

// profileRedemptionCredentials hands out a different token for the default
// login and for each profile, and records which was asked for.
type profileRedemptionCredentials struct {
	profiles map[string]Credential
	err      error
	asked    *[]string
}

func (c profileRedemptionCredentials) Access(context.Context, string) (Credential, error) {
	panic("redemption must never refresh")
}

func (c profileRedemptionCredentials) AccessWithoutRefresh(context.Context, string) (Credential, error) {
	*c.asked = append(*c.asked, "default")
	return Credential{AccessToken: "default-token"}, nil
}

func (c profileRedemptionCredentials) AccessClaudeProfile(_ context.Context, dir string) (Credential, error) {
	*c.asked = append(*c.asked, dir)
	if c.err != nil {
		return Credential{}, c.err
	}
	return c.profiles[dir], nil
}

// A Claude Code profile account spends a reset from its own login: the
// organization lookup and the redeem POST both carry the profile's token,
// and the default login is never read.
func TestProfileAccountRedeemsFromItsOwnLogin(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		if r.URL.Path == "/api/oauth/profile" {
			fmt.Fprint(w, `{"organization":{"uuid":"12345678-1234-1234-1234-123456789abc"}}`)
			return
		}
		fmt.Fprint(w, `{"result":"reset","cleared":["seven_day"],"resets_left":0}`)
	}))
	defer server.Close()
	var asked []string
	r := NewRedeemer(profileRedemptionCredentials{
		profiles: map[string]Credential{"/p/.claude-cc-frank": {AccessToken: "frank-token"}},
		asked:    &asked,
	})
	r.claudeBaseURL = server.URL
	got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-frank"},
		RedeemRequest{ResetID: "grant_1", RequestID: "request-1"})
	if err != nil || got.Outcome != "redeemed" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if len(asked) != 1 || asked[0] != "/p/.claude-cc-frank" {
		t.Fatalf("credentials read: %v", asked)
	}
	if len(tokens) != 2 || tokens[0] != "Bearer frank-token" || tokens[1] != "Bearer frank-token" {
		t.Fatalf("requests carried %v", tokens)
	}
}

// A profile whose login cannot be read without refreshing says so and sends
// nothing: Redline never refreshes a profile's credential.
func TestProfileRedemptionNeedsTheProfilesOwnSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request sent without a usable profile login")
	}))
	defer server.Close()
	var asked []string
	r := NewRedeemer(profileRedemptionCredentials{err: profileNeedsSessionError{dir: "~/.claude-cc-frank"}, asked: &asked})
	r.claudeBaseURL = server.URL
	got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-frank"},
		RedeemRequest{ResetID: "grant_1", RequestID: "request-1"})
	if err != nil || got.Outcome != "failed" || got.Reason != (profileNeedsSessionError{dir: "~/.claude-cc-frank"}).Error() {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if len(asked) != 1 || asked[0] == "default" {
		t.Fatalf("credentials read: %v", asked)
	}
}

// Credentials without profile support refuse a profile account rather than
// fall back to the default login.
func TestProfileRedemptionNeverFallsBackToTheDefaultLogin(t *testing.T) {
	r := NewRedeemer(redemptionCredentials{credential: Credential{AccessToken: "default-token"}})
	r.claudeBaseURL = "http://127.0.0.1:1"
	got, err := r.Redeem(context.Background(), config.Provider{Provider: "claude", ClaudeConfigDir: "/p/.claude-cc-frank"},
		RedeemRequest{ResetID: "grant_1", RequestID: "request-1"})
	if err != nil || got.Outcome != "failed" || got.Reason != "claude profile credentials are unavailable" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
