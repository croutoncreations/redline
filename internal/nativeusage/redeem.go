package nativeusage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/google/uuid"
)

// ErrIndeterminate means the provider answered but did not say whether the
// reset was spent (an unrecognized or explicitly indeterminate result). The
// caller must keep the request pending and retry with the same request ID,
// never treat it as a definitive failure that frees a new key.
var ErrIndeterminate = errors.New("redemption outcome is indeterminate")

type RedeemRequest struct {
	ResetID   string
	RequestID string
	DryRun    bool
	// Only Prepare may preserve dummy IDs across the write-before-send boundary.
	preparedDryRun bool
}

type RedeemResult struct {
	Outcome       string     `json:"outcome"`
	Code          string     `json:"code"`
	Reason        string     `json:"reason"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	Cleared       []string   `json:"cleared,omitempty"`
	ResetsLeft    *int       `json:"resets_left,omitempty"`
	ResetID       string     `json:"reset_id"`
	RequestID     string     `json:"request_id"`
	Verified      bool       `json:"verified"`
	Replayed      bool       `json:"replayed,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// A replay returns only the stored outcome; it makes no new verification claim.
func (r RedeemResult) MarshalJSON() ([]byte, error) {
	type result RedeemResult
	var verified *bool
	if !r.Replayed {
		verified = &r.Verified
	}
	return json.Marshal(struct {
		result
		Verified *bool `json:"verified,omitempty"`
	}{result: result(r), Verified: verified})
}

type Redeemer struct {
	credentials ReadOnlyCredentials
	httpClient  *http.Client
	// These overrides are deliberately private: bearer tokens must never be
	// sent to a user-configured host.
	claudeBaseURL   string
	codexConsumeURL string
	mu              sync.Mutex
	organizations   map[string]string
}

func NewRedeemer(credentials ReadOnlyCredentials) *Redeemer {
	return &Redeemer{credentials: credentials, organizations: make(map[string]string)}
}

var (
	grantIDPattern     = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	dryCreditIDPattern = regexp.MustCompile(`^redline-dryrun-[a-f0-9]{32}$`)
	requestIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	orgIDPattern       = regexp.MustCompile(`^[A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{12}$`)
	// Fixed application namespace; changing it would change retry keys.
	redemptionNamespace = uuid.MustParse("fb19a836-73b8-5af1-b93e-29b70fc84bae")
)

// Prepare allocates the IDs that must be persisted before Redeem is called.
// Prepared dry-run IDs are preserved by Redeem, and can never name a real reset.
func (r *Redeemer) Prepare(ctx context.Context, provider config.Provider, resetID string, dryRun bool) (RedeemRequest, error) {
	req := RedeemRequest{ResetID: resetID, RequestID: uuid.NewString(), DryRun: dryRun}
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	if dryRun {
		return dryRequest(name, req), nil
	}
	if name == "codex" {
		credential, err := r.credential(ctx, name)
		if err != nil {
			return req, err
		}
		account := credential.AccountID
		if account == "" {
			account = "local"
		}
		req.RequestID = uuid.NewSHA1(redemptionNamespace, []byte(account+":"+resetID)).String()
	}
	return req, nil
}

func dryRequest(name string, req RedeemRequest) RedeemRequest {
	if name == "claude" {
		req.ResetID = "redline-dryrun"
	} else if !req.preparedDryRun || !dryCreditIDPattern.MatchString(req.ResetID) {
		req.ResetID = "redline-dryrun-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		req.RequestID = uuid.NewString()
	}
	if !requestIDPattern.MatchString(req.RequestID) {
		req.RequestID = uuid.NewString()
	}
	req.preparedDryRun = true
	return req
}

func (r *Redeemer) credential(ctx context.Context, name string) (Credential, error) {
	if r.credentials == nil {
		return Credential{}, fmt.Errorf("native credentials are unavailable")
	}
	c, err := r.credentials.AccessWithoutRefresh(ctx, name)
	if name == "claude" && err != nil {
		return Credential{}, fmt.Errorf("Claude login needs refreshing; run claude to refresh, then retry or redeem in Claude Code (/limit-reset)")
	}
	if name == "codex" && ((err != nil && strings.Contains(err.Error(), "credentials are invalid")) || strings.HasPrefix(c.AccessToken, "sk-")) {
		return Credential{}, fmt.Errorf("Codex reset credits require a ChatGPT login")
	}
	if err != nil {
		return Credential{}, err
	}
	if c.AccessToken == "" {
		return Credential{}, fmt.Errorf("%s access token is unavailable", name)
	}
	return c, nil
}

func (r *Redeemer) Redeem(ctx context.Context, provider config.Provider, req RedeemRequest) (RedeemResult, error) {
	name := strings.ToLower(strings.TrimSpace(provider.Provider))
	if name != "claude" && name != "codex" {
		return RedeemResult{}, fmt.Errorf("native redemption for %q is unsupported", name)
	}
	if req.DryRun {
		req = dryRequest(name, req)
	}
	result := RedeemResult{Outcome: "failed", ResetID: req.ResetID, RequestID: req.RequestID}
	if !requestIDPattern.MatchString(req.RequestID) || (name == "claude" && !grantIDPattern.MatchString(req.ResetID)) || req.ResetID == "" {
		return result, fmt.Errorf("invalid reset id or request id")
	}
	credential, err := r.credential(ctx, name)
	if err != nil {
		result.Reason = err.Error()
		return result, nil
	}
	timeout := 20 * time.Second
	if name == "claude" {
		timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint := r.codexConsumeURL
	if endpoint == "" {
		endpoint = defaultCodexResetCreditsURL + "/consume"
	}
	var payload any = map[string]string{"credit_id": req.ResetID, "redeem_request_id": req.RequestID}
	if name == "claude" {
		org, err := r.organization(ctx, credential)
		if err != nil {
			return redemptionError(result, err)
		}
		endpoint = r.claudeBase() + "/api/organizations/" + org + "/reset_rate_limits"
		payload = map[string]string{"program": "cedar_ember", "grant_id": req.ResetID, "request_id": req.RequestID}
	}
	body, _ := json.Marshal(payload)
	response, err := r.request(ctx, name, http.MethodPost, endpoint, credential, body)
	if err != nil {
		return redemptionError(result, err)
	}
	var data struct {
		Result        string     `json:"result"`
		Code          string     `json:"code"`
		Reason        string     `json:"reason"`
		CooldownUntil *time.Time `json:"cooldown_until"`
		Cleared       []string   `json:"cleared"`
		ResetsLeft    *int       `json:"resets_left"`
	}
	if err := json.Unmarshal(response, &data); err != nil {
		return result, fmt.Errorf("decode %s redemption: %w", name, err)
	}
	result.Code = data.Code
	if name == "claude" {
		result.Code = data.Result
	}
	result.Reason, result.CooldownUntil, result.ResetsLeft = data.Reason, data.CooldownUntil, data.ResetsLeft
	// Reuse the snapshot normalization for window names and deduplication.
	var normalized decision.UsageSnapshot
	normalized.ApplyBankedResets(&decision.BankedResets{Details: []decision.BankedReset{{Clears: data.Cleared}}})
	result.Cleared = normalized.BankedResetDetails[0].Clears
	// Only results known to mean "not spent" may end the request as failed.
	// Anything else might follow an applied redemption, so it stays pending.
	determinate := true
	if name == "claude" {
		switch result.Code {
		case "reset", "already_used":
			result.Outcome = "redeemed"
		case "not_limited":
			result.Outcome = "nothing_to_reset"
		case "cooldown", "paused", "low_priority_active":
			result.Outcome = "cooldown"
		case "no_grant", "unknown_grant", "not_next_grant":
			result.Outcome = "gone"
		case "ineligible", "unavailable":
		default:
			determinate = false
		}
		if req.DryRun && (result.Code == "ineligible" || result.Code == "unknown_grant") {
			result.Outcome = "dry_run_ok"
		}
	} else {
		switch result.Code {
		case "reset", "already_redeemed":
			result.Outcome = "redeemed"
		case "nothing_to_reset":
			result.Outcome = "nothing_to_reset"
		case "no_credit":
			result.Outcome = "gone"
		default:
			determinate = false
		}
		if req.DryRun && result.Code == "no_credit" {
			result.Outcome = "dry_run_ok"
		}
	}
	if !determinate && !req.DryRun {
		return result, fmt.Errorf("%w: %s returned %q", ErrIndeterminate, name, result.Code)
	}
	return result, nil
}

func redemptionError(result RedeemResult, err error) (RedeemResult, error) {
	if e, ok := err.(*HTTPError); ok && (e.StatusCode == 401 || e.StatusCode == 403) {
		result.Reason = "Provider authentication failed; refresh your CLI login, then retry"
		result.Code = fmt.Sprint(e.StatusCode)
		return result, nil
	}
	return result, err
}

func (r *Redeemer) claudeBase() string {
	if r.claudeBaseURL != "" {
		return r.claudeBaseURL
	}
	return "https://api.anthropic.com"
}

func (r *Redeemer) organization(ctx context.Context, credential Credential) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if org := r.organizations[credential.AccessToken]; org != "" {
		return org, nil
	}
	body, err := r.request(ctx, "claude", http.MethodGet, r.claudeBase()+"/api/oauth/profile", credential, nil)
	if err != nil {
		return "", err
	}
	var profile struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &profile); err != nil {
		return "", fmt.Errorf("decode Claude profile: %w", err)
	}
	org := strings.TrimSpace(profile.Organization.UUID)
	if !orgIDPattern.MatchString(org) {
		return "", fmt.Errorf("Claude profile has an invalid organization UUID")
	}
	r.organizations[credential.AccessToken] = org
	return org, nil
}

func (r *Redeemer) request(ctx context.Context, name, method, endpoint string, credential Credential, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if name == "claude" {
		req.Header.Set("User-Agent", claudeUserAgent)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	} else {
		req.Header.Set("OpenAI-Beta", "codex-1")
		if credential.AccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", credential.AccountID)
		}
	}
	client := r.httpClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Status and Retry-After remain authoritative even if the error body
	// cannot be read. No provider body is needed for the HTTPError message.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Provider: name, StatusCode: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	const responseLimit = 2 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > responseLimit {
		return nil, fmt.Errorf("%s redemption response too large", name)
	}
	return data, nil
}
