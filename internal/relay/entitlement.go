package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

const (
	EntitlementLifetime = 14 * 24 * time.Hour
	// EntitlementClockSkew is the maximum difference tolerated between the
	// issuer, relay, and desktop clocks. It is deliberately small: it prevents
	// a harmless clock offset from rejecting a fresh credential without
	// extending the issuer's fourteen-day lifetime by more than five minutes.
	EntitlementClockSkew = 5 * time.Minute
	maxIssuerBody        = 64 << 10
	maxEntitlementToken  = 16 << 10
	maxActivationCount   = 25
)

// Secret is printable only as a redaction marker. JSON encoding is supported
// solely for the issuer wire response and the protected entitlement cache.
type Secret struct{ value string }

func NewSecret(value string) Secret { return Secret{value: value} }
func (s Secret) Value() string      { return s.value }
func (s Secret) String() string {
	if s.value == "" {
		return ""
	}
	return "[REDACTED]"
}
func (s Secret) GoString() string             { return s.String() }
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.value) }
func (s *Secret) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	s.value = value
	return nil
}

// SessionSID derives the issuer/relay binding without exposing session_id.
func SessionSID(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type ActivationSummary struct {
	Label     string    `json:"label"`
	FirstSeen time.Time `json:"first_seen"`
}

type Activation struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	FirstSeen time.Time `json:"first_seen"`
	Current   bool      `json:"current"`
}

type Entitlement struct {
	Token      Secret `json:"token"`
	Exp        int64  `json:"exp"`
	MaxClients int    `json:"max_clients"`
	Seats      int    `json:"seats"`
	SeatsUsed  int    `json:"seats_used"`
}

// ReceivedEntitlement binds an issuer response to the instant at which its
// complete HTTP body was received. Renewal authority must use this instant,
// not a caller sample taken before Keychain or network latency.
type ReceivedEntitlement struct {
	Entitlement
	ReceivedAt time.Time
}

type IssuerErrorKind string

const (
	IssuerInvalidKey      IssuerErrorKind = "invalid_key"
	IssuerLapsed          IssuerErrorKind = "lapsed"
	IssuerNoSeat          IssuerErrorKind = "no_seat"
	IssuerUnavailable     IssuerErrorKind = "unavailable"
	IssuerRateLimited     IssuerErrorKind = "rate_limited"
	IssuerNotFound        IssuerErrorKind = "not_found"
	IssuerConflict        IssuerErrorKind = "conflict"
	IssuerInvalidResponse IssuerErrorKind = "invalid_response"
)

// IssuerError deliberately excludes response bodies and request credentials.
type IssuerError struct {
	Kind        IssuerErrorKind
	Status      int
	Retryable   bool
	Activations []ActivationSummary
	cause       error
}

func (e *IssuerError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("issuer request failed: %s (HTTP %d)", e.Kind, e.Status)
	}
	return fmt.Sprintf("issuer request failed: %s", e.Kind)
}
func (e *IssuerError) Unwrap() error { return e.cause }

type issuerEntitlementRequest struct {
	LicenseKey string `json:"license_key"`
	SID        string `json:"sid"`
	Label      string `json:"label"`
}

type activationsResponse struct {
	Activations []Activation `json:"activations"`
}

type noSeatResponse struct {
	Activations *[]ActivationSummary `json:"activations"`
}

type portalResponse struct {
	URL string `json:"url"`
}

// IssuerClient implements only the public issuer contract. Its HTTP client
// must have a timeout; NewIssuerClient supplies one when the caller does not.
type IssuerClient struct {
	base  *url.URL
	http  *http.Client
	clock issuerClock
}

type issuerClock interface {
	Now() time.Time
}

type realIssuerClock struct{}

func (realIssuerClock) Now() time.Time { return time.Now() }

func NewIssuerClient(rawBase string, client *http.Client) (*IssuerClient, error) {
	return newIssuerClient(rawBase, client, realIssuerClock{})
}

func newIssuerClient(rawBase string, client *http.Client, clock issuerClock) (*IssuerClient, error) {
	base, err := safeHTTPSURL(rawBase)
	if err != nil {
		return nil, fmt.Errorf("issuer URL: %w", err)
	}
	if base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, fmt.Errorf("issuer URL must not contain credentials, query, or fragment")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	} else {
		clone := *client
		client = &clone
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	}
	origin := originOf(base)
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || originOf(req.URL) != origin {
			return errors.New("issuer redirect changed origin or scheme")
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many issuer redirects")
		}
		return nil
	}
	if clock == nil {
		clock = realIssuerClock{}
	}
	return &IssuerClient{base: base, http: client, clock: clock}, nil
}

func safeHTTPSURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return nil, errors.New("must not contain credentials")
	}
	return parsed, nil
}

func originOf(u *url.URL) string {
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func (c *IssuerClient) endpoint(parts ...string) string {
	copy := *c.base
	segments := append([]string{copy.Path}, parts...)
	copy.Path = path.Join(segments...)
	return copy.String()
}

func (c *IssuerClient) activationEndpoint(id string) string {
	copy := *c.base
	prefix := path.Join(copy.Path, "v1", "activations")
	copy.Path = prefix + "/" + id
	copy.RawPath = (&url.URL{Path: prefix}).EscapedPath() + "/" + url.PathEscape(id)
	return copy.String()
}

func (c *IssuerClient) Entitlement(ctx context.Context, licenseKey, sid, label string) (ReceivedEntitlement, error) {
	body, err := json.Marshal(issuerEntitlementRequest{LicenseKey: licenseKey, SID: sid, Label: label})
	if err != nil {
		return ReceivedEntitlement{}, &IssuerError{Kind: IssuerInvalidResponse, cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("v1", "entitlement"), bytes.NewReader(body))
	if err != nil {
		return ReceivedEntitlement{}, &IssuerError{Kind: IssuerInvalidResponse, cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, raw, err := c.do(req)
	// The issuer client owns this sample. It is taken only after do has finished
	// reading the response body (or the transport attempt has completed).
	receivedAt := c.clock.Now()
	if err != nil {
		return ReceivedEntitlement{ReceivedAt: receivedAt}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var result Entitlement
		if err := decodeStrict(raw, &result); err != nil {
			return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode, cause: err}
		}
		if err := ValidateEntitlementAt(result, sid, receivedAt); err != nil {
			return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode, cause: err}
		}
		return ReceivedEntitlement{Entitlement: result, ReceivedAt: receivedAt}, nil
	case http.StatusUnauthorized:
		return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerInvalidKey, Status: resp.StatusCode}
	case http.StatusPaymentRequired:
		return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerLapsed, Status: resp.StatusCode}
	case http.StatusConflict:
		var result noSeatResponse
		if err := decodeStrict(raw, &result); err != nil || result.Activations == nil || !validActivationSummaries(*result.Activations) {
			return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
		}
		return ReceivedEntitlement{ReceivedAt: receivedAt}, &IssuerError{Kind: IssuerNoSeat, Status: resp.StatusCode, Activations: *result.Activations}
	default:
		return ReceivedEntitlement{ReceivedAt: receivedAt}, classifyStatus(resp.StatusCode)
	}
}

func (c *IssuerClient) Activations(ctx context.Context, licenseKey string) ([]Activation, error) {
	req, err := c.authorizedRequest(ctx, http.MethodGet, c.endpoint("v1", "activations"), licenseKey)
	if err != nil {
		return nil, err
	}
	resp, raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyStatus(resp.StatusCode)
	}
	var result activationsResponse
	if err := decodeStrict(raw, &result); err != nil || len(result.Activations) > maxActivationCount {
		return nil, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
	}
	for _, activation := range result.Activations {
		if !validActivationID(activation.ID) || activation.FirstSeen.IsZero() || !validLabel(activation.Label) {
			return nil, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
		}
	}
	return result.Activations, nil
}

func (c *IssuerClient) DeleteActivation(ctx context.Context, licenseKey, id string) error {
	if !validActivationID(id) {
		return &IssuerError{Kind: IssuerInvalidResponse}
	}
	req, err := c.authorizedRequest(ctx, http.MethodDelete, c.activationEndpoint(id), licenseKey)
	if err != nil {
		return err
	}
	resp, _, err := c.do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent {
		return classifyStatus(resp.StatusCode)
	}
	return nil
}

func (c *IssuerClient) Portal(ctx context.Context, licenseKey string) (*url.URL, error) {
	req, err := c.authorizedRequest(ctx, http.MethodPost, c.endpoint("v1", "portal"), licenseKey)
	if err != nil {
		return nil, err
	}
	resp, raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyStatus(resp.StatusCode)
	}
	var result portalResponse
	if err := decodeStrict(raw, &result); err != nil {
		return nil, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
	}
	portal, err := safeHTTPSURL(result.URL)
	if err != nil || portal.Fragment != "" {
		return nil, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
	}
	return portal, nil
}

func (c *IssuerClient) authorizedRequest(ctx context.Context, method, endpoint, key string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, &IssuerError{Kind: IssuerInvalidResponse, cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	return req, nil
}

func (c *IssuerClient) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &IssuerError{Kind: IssuerUnavailable, Retryable: true, cause: errors.New("transport failure")}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIssuerBody+1))
	if err != nil || len(raw) > maxIssuerBody {
		return nil, nil, &IssuerError{Kind: IssuerInvalidResponse, Status: resp.StatusCode}
	}
	return resp, raw, nil
}

func validActivationID(id string) bool {
	if id == "" || len(id) > 512 {
		return false
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validLabel(label string) bool {
	return len(label) <= 200 && strings.IndexFunc(label, unicode.IsControl) < 0
}

func validActivationSummaries(activations []ActivationSummary) bool {
	if len(activations) > maxActivationCount {
		return false
	}
	for _, activation := range activations {
		if !validLabel(activation.Label) || activation.FirstSeen.IsZero() {
			return false
		}
	}
	return true
}

func classifyStatus(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return &IssuerError{Kind: IssuerInvalidKey, Status: status}
	case http.StatusPaymentRequired:
		return &IssuerError{Kind: IssuerLapsed, Status: status}
	case http.StatusTooManyRequests:
		return &IssuerError{Kind: IssuerRateLimited, Status: status, Retryable: true}
	case http.StatusNotFound:
		return &IssuerError{Kind: IssuerNotFound, Status: status}
	case http.StatusConflict:
		return &IssuerError{Kind: IssuerConflict, Status: status}
	}
	if status >= 500 {
		return &IssuerError{Kind: IssuerUnavailable, Status: status, Retryable: true}
	}
	return &IssuerError{Kind: IssuerInvalidResponse, Status: status}
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	return nil
}

type tokenClaims struct {
	Exp        int64  `json:"exp"`
	SID        string `json:"sid"`
	MaxClients int    `json:"max_clients"`
}

// ValidateEntitlementAt validates issuer response coherence at the instant the
// complete response was received. Callers must not pass the request start time:
// network latency is not part of the credential lifetime.
func ValidateEntitlementAt(entitlement Entitlement, sid string, receivedAt time.Time) error {
	decodedSID, sidErr := base64.RawURLEncoding.DecodeString(sid)
	if sidErr != nil || len(decodedSID) != sha256.Size {
		return errors.New("sid is malformed")
	}
	if entitlement.Token.Value() == "" || len(entitlement.Token.Value()) > maxEntitlementToken {
		return errors.New("token is missing or too large")
	}
	if entitlement.MaxClients < 1 || entitlement.MaxClients > 25 || entitlement.Seats < 1 || entitlement.Seats > 10000 || entitlement.SeatsUsed < 1 || entitlement.SeatsUsed > entitlement.Seats {
		return errors.New("entitlement values are out of range")
	}
	expiresAt := time.Unix(entitlement.Exp, 0)
	if !expiresAt.After(receivedAt.Add(-EntitlementClockSkew)) || expiresAt.After(receivedAt.Add(EntitlementLifetime+EntitlementClockSkew)) {
		return errors.New("entitlement expiration is out of range")
	}
	parts := strings.Split(entitlement.Token.Value(), ".")
	if len(parts) != 2 || parts[1] == "" {
		return errors.New("token format is invalid")
	}
	signature, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		signature, err = base64.RawStdEncoding.DecodeString(parts[1])
	}
	if err != nil || len(signature) != 64 {
		return errors.New("token signature is malformed")
	}
	claimsRaw, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		claimsRaw, err = base64.RawStdEncoding.DecodeString(parts[0])
	}
	if err != nil {
		return errors.New("token claims encoding is invalid")
	}
	var claims tokenClaims
	if err := decodeStrict(claimsRaw, &claims); err != nil {
		return errors.New("token claims are invalid")
	}
	if claims.Exp != entitlement.Exp || claims.SID != sid || claims.MaxClients != entitlement.MaxClients {
		return errors.New("token and issuer response disagree")
	}
	return nil
}

type RelayRefreshErrorKind string

const (
	RelayRefreshNoHost      RelayRefreshErrorKind = "no_host"
	RelayRefreshAuth        RelayRefreshErrorKind = "auth"
	RelayRefreshUnavailable RelayRefreshErrorKind = "unavailable"
	RelayRefreshRejected    RelayRefreshErrorKind = "rejected"
)

type RelayRefreshError struct {
	Kind   RelayRefreshErrorKind
	Status int
}

func (e *RelayRefreshError) Error() string {
	return fmt.Sprintf("relay entitlement refresh failed: %s (HTTP %d)", e.Kind, e.Status)
}

func RefreshRelayEntitlement(ctx context.Context, client *http.Client, relayURL, sessionID string, token Secret) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return &RelayRefreshError{Kind: RelayRefreshRejected}
	}
	base, err := safeHTTPSURL(relayURL)
	if err != nil || base.RawQuery != "" || base.Fragment != "" {
		return &RelayRefreshError{Kind: RelayRefreshRejected}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	} else {
		clone := *client
		client = &clone
		if client.Timeout <= 0 || client.Timeout > 15*time.Second {
			client.Timeout = 15 * time.Second
		}
	}
	// Never follow a refresh redirect. The entitlement is a bearer credential,
	// and even same-origin redirects create unnecessary opportunities for it to
	// reach an endpoint that did not request it.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	target := *base
	prefix := path.Join(target.Path, "v1", "session")
	target.Path = prefix + "/" + sessionID + "/entitlement"
	target.RawPath = (&url.URL{Path: prefix}).EscapedPath() + "/" + url.PathEscape(sessionID) + "/entitlement"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), nil)
	if err != nil {
		return &RelayRefreshError{Kind: RelayRefreshRejected}
	}
	req.Header.Set("X-Redline-Entitlement", token.Value())
	resp, err := client.Do(req)
	if err != nil {
		return &RelayRefreshError{Kind: RelayRefreshUnavailable}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxIssuerBody))
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusLocked:
		return &RelayRefreshError{Kind: RelayRefreshNoHost, Status: resp.StatusCode}
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
		return &RelayRefreshError{Kind: RelayRefreshAuth, Status: resp.StatusCode}
	default:
		if resp.StatusCode >= 500 {
			return &RelayRefreshError{Kind: RelayRefreshUnavailable, Status: resp.StatusCode}
		}
		return &RelayRefreshError{Kind: RelayRefreshRejected, Status: resp.StatusCode}
	}
}
