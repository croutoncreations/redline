package nativeusage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errCredentialsChanged = errors.New("credentials changed concurrently")

// ErrSignedOut means Claude Code has no usable login on this machine: its
// credential is missing, emptied (what a sign-out leaves behind), or expired
// with nothing to refresh it. Nothing Redline can do brings it back, so the
// message says what happened and the command that fixes it, and every
// surface shows it as-is.
var ErrSignedOut error = signedOutError{}

type signedOutError struct{}

func (signedOutError) Error() string {
	return "Claude Code is signed out on this Mac; run `claude auth login` to sign in again"
}

// Reason is the machine-readable cause the usage manager reports beside the
// message, so clients can offer the fix without parsing prose.
func (signedOutError) Reason() string { return "signed_out" }

// errNoCredential is what a credential store returns when no credential
// exists at all, as opposed to one it could not read (a locked keychain, a
// permission error), which is not a sign-out and keeps its own message.
var errNoCredential = errors.New("no credential is stored")

const (
	claudeRefreshURL = "https://platform.claude.com/v1/oauth/token"
	claudeClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	codexRefreshURL  = "https://auth.openai.com/oauth/token"
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
)

type secretStore interface {
	Read(context.Context) ([]byte, error)
}

type writableSecretStore interface {
	secretStore
	CompareAndSwap(context.Context, []byte, []byte) error
}

type DefaultCredentials struct {
	HTTPClient       *http.Client
	Now              func() time.Time
	ClaudeStore      secretStore
	CodexStore       secretStore
	ClaudeRefreshURL string
	CodexRefreshURL  string
}

func NewDefaultCredentials() *DefaultCredentials {
	home, _ := os.UserHomeDir()
	user := os.Getenv("USER")
	return &DefaultCredentials{
		HTTPClient: &http.Client{Timeout: 15 * time.Second}, Now: time.Now,
		ClaudeStore: newClaudeSecretStore(home, user),
		// Codex CLI stores its OAuth credentials at ~/.codex/auth.json on
		// macOS, Linux, and Windows (os.UserHomeDir resolves %USERPROFILE% on
		// Windows). ~/.config/codex/auth.json is also checked first for Linux
		// installs that follow the XDG base directory convention.
		CodexStore: firstFileStore{Paths: []string{
			filepath.Join(home, ".config", "codex", "auth.json"),
			filepath.Join(home, ".codex", "auth.json"),
		}},
	}
}

func (d *DefaultCredentials) Access(ctx context.Context, provider string) (Credential, error) {
	switch strings.ToLower(provider) {
	case "claude":
		return d.claude(ctx)
	case "codex":
		return d.codex(ctx)
	default:
		return Credential{}, fmt.Errorf("native credentials for %q are unsupported", provider)
	}
}

// readClaudeCredentials reads and decodes Claude Code's credential. A missing
// credential, or one whose access token is empty, is ErrSignedOut; a store
// that cannot be read for any other reason keeps that reason.
func readClaudeCredentials(ctx context.Context, store secretStore) (claudeCredentialsFile, error) {
	raw, err := store.Read(ctx)
	if errors.Is(err, errNoCredential) || errors.Is(err, os.ErrNotExist) {
		return claudeCredentialsFile{}, ErrSignedOut
	}
	if err != nil {
		return claudeCredentialsFile{}, fmt.Errorf("read Claude credentials: %w", err)
	}
	var file claudeCredentialsFile
	if json.Unmarshal(raw, &file) != nil {
		return claudeCredentialsFile{}, fmt.Errorf("claude credentials are invalid")
	}
	if strings.TrimSpace(file.ClaudeAIOAuth.AccessToken) == "" {
		return claudeCredentialsFile{}, ErrSignedOut
	}
	file.raw = raw
	return file, nil
}

type claudeCredentialsFile struct {
	raw           []byte
	ClaudeAIOAuth struct {
		AccessToken      string   `json:"accessToken"`
		RefreshToken     string   `json:"refreshToken"`
		ExpiresAt        float64  `json:"expiresAt"`
		SubscriptionType string   `json:"subscriptionType,omitempty"`
		RateLimitTier    string   `json:"rateLimitTier,omitempty"`
		Scopes           []string `json:"scopes,omitempty"`
	} `json:"claudeAiOauth"`
}

// AccessWithoutRefresh returns the current credential but never refreshes or
// rewrites it. Used for supplementary lookups that must not rotate Claude
// Code's shared refresh token behind its back: a token near expiry is simply
// reported as unavailable, and the next Claude Code session will refresh it.
func (d *DefaultCredentials) AccessWithoutRefresh(ctx context.Context, provider string) (Credential, error) {
	if !strings.EqualFold(provider, "claude") {
		return d.Access(ctx, provider)
	}
	store := d.ClaudeStore
	if store == nil {
		return Credential{}, fmt.Errorf("claude credential store is unavailable")
	}
	file, err := readClaudeCredentials(ctx, store)
	if err != nil {
		return Credential{}, err
	}
	if file.ClaudeAIOAuth.ExpiresAt > 0 && time.UnixMilli(int64(file.ClaudeAIOAuth.ExpiresAt)).Sub(d.now()) <= 5*time.Minute {
		if file.ClaudeAIOAuth.RefreshToken == "" {
			return Credential{}, ErrSignedOut
		}
		return Credential{}, fmt.Errorf("claude token is near expiry; skipping lookup rather than refreshing Claude Code's credential")
	}
	return Credential{AccessToken: file.ClaudeAIOAuth.AccessToken}, nil
}

func (d *DefaultCredentials) claude(ctx context.Context) (Credential, error) {
	store := d.ClaudeStore
	if store == nil {
		return Credential{}, fmt.Errorf("claude credential store is unavailable")
	}
	file, err := readClaudeCredentials(ctx, store)
	if err != nil {
		return Credential{}, err
	}
	raw := file.raw
	now := d.now()
	if file.ClaudeAIOAuth.ExpiresAt > 0 && time.UnixMilli(int64(file.ClaudeAIOAuth.ExpiresAt)).Sub(now) <= 5*time.Minute {
		// Nothing can refresh it: signed out, whether or not the store is
		// writable. Checked first so the read-only macOS store reports it.
		if file.ClaudeAIOAuth.RefreshToken == "" {
			return Credential{}, ErrSignedOut
		}
		writable, ok := store.(writableSecretStore)
		if !ok {
			return Credential{}, fmt.Errorf("claude credentials require refresh; Redline will not modify Claude Code's shared credential - run `claude auth login`")
		}
		payload := map[string]any{"grant_type": "refresh_token", "refresh_token": file.ClaudeAIOAuth.RefreshToken, "client_id": claudeClientID,
			"scope": "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"}
		body, _ := json.Marshal(payload)
		endpoint := d.ClaudeRefreshURL
		if endpoint == "" {
			endpoint = claudeRefreshURL
		}
		response, err := d.request(ctx, http.MethodPost, endpoint, "application/json", body)
		if err != nil {
			return Credential{}, fmt.Errorf("refresh Claude token: %w", err)
		}
		var refreshed struct {
			AccessToken  string  `json:"access_token"`
			RefreshToken string  `json:"refresh_token"`
			ExpiresIn    float64 `json:"expires_in"`
		}
		if json.Unmarshal(response, &refreshed) != nil || refreshed.AccessToken == "" {
			return Credential{}, fmt.Errorf("refresh Claude token: invalid response")
		}
		file.ClaudeAIOAuth.AccessToken = refreshed.AccessToken
		if refreshed.RefreshToken != "" {
			file.ClaudeAIOAuth.RefreshToken = refreshed.RefreshToken
		}
		if refreshed.ExpiresIn > 0 {
			file.ClaudeAIOAuth.ExpiresAt = float64(now.UnixMilli()) + refreshed.ExpiresIn*1000
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			return Credential{}, fmt.Errorf("preserve Claude credentials: %w", err)
		}
		oauth, ok := document["claudeAiOauth"].(map[string]any)
		if !ok {
			return Credential{}, fmt.Errorf("preserve Claude credentials: oauth object is missing")
		}
		oauth["accessToken"] = file.ClaudeAIOAuth.AccessToken
		oauth["refreshToken"] = file.ClaudeAIOAuth.RefreshToken
		oauth["expiresAt"] = file.ClaudeAIOAuth.ExpiresAt
		updated, _ := json.Marshal(document)
		if err := writable.CompareAndSwap(ctx, raw, updated); err != nil {
			return Credential{}, fmt.Errorf("persist Claude token refresh: %w", err)
		}
	}
	return Credential{AccessToken: file.ClaudeAIOAuth.AccessToken}, nil
}

type codexAuthFile struct {
	Tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

func (d *DefaultCredentials) codex(ctx context.Context) (Credential, error) {
	store := d.CodexStore
	if store == nil {
		return Credential{}, fmt.Errorf("codex credential store is unavailable")
	}
	raw, err := store.Read(ctx)
	if err != nil {
		return Credential{}, fmt.Errorf("read Codex credentials: %w", err)
	}
	var file codexAuthFile
	if json.Unmarshal(raw, &file) != nil || file.Tokens.AccessToken == "" {
		return Credential{}, fmt.Errorf("codex credentials are invalid")
	}
	now := d.now()
	if expiresAt, ok := jwtExpiry(file.Tokens.AccessToken); ok && expiresAt.Sub(now) <= 5*time.Minute {
		writable, ok := store.(writableSecretStore)
		if !ok {
			return Credential{}, fmt.Errorf("codex credential store is read-only")
		}
		if file.Tokens.RefreshToken == "" {
			return Credential{}, fmt.Errorf("codex token expired without a refresh token")
		}
		values := url.Values{"grant_type": {"refresh_token"}, "client_id": {codexClientID}, "refresh_token": {file.Tokens.RefreshToken}}
		endpoint := d.CodexRefreshURL
		if endpoint == "" {
			endpoint = codexRefreshURL
		}
		response, err := d.request(ctx, http.MethodPost, endpoint, "application/x-www-form-urlencoded", []byte(values.Encode()))
		if err != nil {
			return Credential{}, fmt.Errorf("refresh Codex token: %w", err)
		}
		var refreshed struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
		}
		if json.Unmarshal(response, &refreshed) != nil || refreshed.AccessToken == "" {
			return Credential{}, fmt.Errorf("refresh Codex token: invalid response")
		}
		file.Tokens.AccessToken = refreshed.AccessToken
		if refreshed.RefreshToken != "" {
			file.Tokens.RefreshToken = refreshed.RefreshToken
		}
		if refreshed.IDToken != "" {
			file.Tokens.IDToken = refreshed.IDToken
		}
		file.LastRefresh = now.UTC().Format(time.RFC3339Nano)
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			return Credential{}, fmt.Errorf("preserve Codex credentials: %w", err)
		}
		tokens, ok := document["tokens"].(map[string]any)
		if !ok {
			return Credential{}, fmt.Errorf("preserve Codex credentials: tokens object is missing")
		}
		tokens["access_token"] = file.Tokens.AccessToken
		tokens["refresh_token"] = file.Tokens.RefreshToken
		if file.Tokens.IDToken != "" {
			tokens["id_token"] = file.Tokens.IDToken
		}
		document["last_refresh"] = file.LastRefresh
		updated, _ := json.MarshalIndent(document, "", "  ")
		if err := writable.CompareAndSwap(ctx, raw, updated); err != nil {
			return Credential{}, fmt.Errorf("persist Codex token refresh: %w", err)
		}
	}
	return Credential{AccessToken: file.Tokens.AccessToken, AccountID: file.Tokens.AccountID}, nil
}

func (d *DefaultCredentials) request(ctx context.Context, method, endpoint, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	client := d.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return response, nil
}

func (d *DefaultCredentials) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(claims.Exp.String(), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0).UTC(), true
}

type firstFileStore struct {
	Paths []string
}

// locate returns the first credential file that exists. A path that cannot
// be checked for a reason other than not existing -- a directory without
// permission -- is reported as that error, not as "no file": the difference
// is whether signing in again would help.
func (s firstFileStore) locate() (string, error) {
	for _, path := range s.Paths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}
func (s firstFileStore) Read(context.Context) ([]byte, error) {
	path, err := s.locate()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
func (s firstFileStore) CompareAndSwap(_ context.Context, old, updated []byte) error {
	path, err := s.locate()
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, old) {
		return errCredentialsChanged
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".redline-auth-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
