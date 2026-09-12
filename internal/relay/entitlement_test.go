package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedIssuerClock struct{ now time.Time }

func (c fixedIssuerClock) Now() time.Time { return c.now }

type mutableIssuerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableIssuerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableIssuerClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}

func testEntitlementToken(t *testing.T, exp int64, sid string, maxClients int) string {
	t.Helper()
	claims, err := json.Marshal(tokenClaims{Exp: exp, SID: sid, MaxClients: maxClients})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(claims) + "." + base64.StdEncoding.EncodeToString(make([]byte, 64))
}

func TestIssuerEntitlementUsesBodyAndValidatesTokenCoherence(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sid := SessionSID("session-issuer-contract-123")
	exp := now.Add(EntitlementLifetime).Unix()
	const license = "rl_live_must_never_leak"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), license) || r.URL.Path != "/api/v1/entitlement" {
			t.Fatalf("unsafe request URL %q", r.URL.String())
		}
		var body issuerEntitlementRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.LicenseKey != license || body.SID != sid || body.Label != "Studio Mac" {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(Entitlement{
			Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
			MaxClients: 5, Seats: 2, SeatsUsed: 1,
		})
	}))
	defer server.Close()
	client, err := newIssuerClient(server.URL+"/api", server.Client(), fixedIssuerClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Entitlement(context.Background(), license, sid, "Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if got.Exp != exp || got.MaxClients != 5 || got.Seats != 2 || got.SeatsUsed != 1 {
		t.Fatalf("entitlement = %#v", got)
	}
	for _, diagnostic := range []string{fmt.Sprint(got), fmt.Sprintf("%#v", got), fmt.Sprint(got.Token)} {
		if strings.Contains(diagnostic, got.Token.Value()) {
			t.Fatalf("token leaked through diagnostic: %s", diagnostic)
		}
	}
}

func TestIssuerEntitlementSamplesReceiptAfterBlockedResponse(t *testing.T) {
	startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	receivedAt := startedAt.Add(2 * EntitlementClockSkew)
	clock := &mutableIssuerClock{now: startedAt}
	sid := SessionSID("session-issuer-receipt-123456")
	exp := receivedAt.Add(EntitlementLifetime).Unix()
	responseStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(responseStarted)
		<-releaseResponse
		_ = json.NewEncoder(w).Encode(Entitlement{
			Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
			MaxClients: 5, Seats: 1, SeatsUsed: 1,
		})
	}))
	defer server.Close()
	client, err := newIssuerClient(server.URL, server.Client(), clock)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan ReceivedEntitlement, 1)
	errs := make(chan error, 1)
	go func() {
		issued, issueErr := client.Entitlement(context.Background(), "license", sid, "")
		result <- issued
		errs <- issueErr
	}()
	<-responseStarted
	clock.Advance(2 * EntitlementClockSkew)
	close(releaseResponse)
	issued := <-result
	if err := <-errs; err != nil {
		t.Fatalf("legitimate fourteen-day response rejected using request time: %v", err)
	}
	if !issued.ReceivedAt.Equal(receivedAt) || issued.Exp != exp {
		t.Fatalf("received entitlement=%#v want receipt=%v", issued, receivedAt)
	}
}

func TestIssuerRejectsAResponseWhoseTokenClaimsDisagree(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("session-coherence-123456")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		responseExp := now.Add(EntitlementLifetime).Unix()
		_ = json.NewEncoder(w).Encode(Entitlement{
			Token: NewSecret(testEntitlementToken(t, responseExp-1, sid, 5)), Exp: responseExp,
			MaxClients: 5, Seats: 1, SeatsUsed: 1,
		})
	}))
	defer server.Close()
	client, _ := newIssuerClient(server.URL, server.Client(), fixedIssuerClock{now: now})
	_, err := client.Entitlement(context.Background(), "license", sid, "")
	var typed *IssuerError
	if !errors.As(err, &typed) || typed.Kind != IssuerInvalidResponse {
		t.Fatalf("error=%#v", err)
	}
}

func TestRedactTokenRemovesLicenseAndEntitlementForms(t *testing.T) {
	license, token := "rl_live_redact_me", "claims.signature"
	message := "license=" + license + " token=" + token + " escaped=" + url.QueryEscape(token)
	got := redactToken(message, license, token)
	if strings.Contains(got, license) || strings.Contains(got, token) || strings.Contains(got, url.QueryEscape(token)) {
		t.Fatalf("credential survived redaction: %q", got)
	}
}

func TestIssuerTypedFailuresNeverIncludeCredentialsOrBodies(t *testing.T) {
	const license = "rl_live_error_secret"
	for status, kind := range map[int]IssuerErrorKind{
		http.StatusUnauthorized:       IssuerInvalidKey,
		http.StatusPaymentRequired:    IssuerLapsed,
		http.StatusServiceUnavailable: IssuerUnavailable,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("response-secret " + license))
			}))
			defer server.Close()
			client, _ := NewIssuerClient(server.URL, server.Client())
			_, err := client.Entitlement(context.Background(), license, SessionSID("session-error-123456"), "")
			var typed *IssuerError
			if !errors.As(err, &typed) || typed.Kind != kind {
				t.Fatalf("error = %#v, want %s", err, kind)
			}
			if strings.Contains(err.Error(), license) || strings.Contains(err.Error(), "response-secret") {
				t.Fatalf("credential/body leaked in %q", err)
			}
		})
	}
}

func TestIssuerNoSeatRequiresActivationsField(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, _ := NewIssuerClient(server.URL, server.Client())
	_, err := client.Entitlement(context.Background(), "license", SessionSID("session-no-seat-123456"), "")
	var typed *IssuerError
	if !errors.As(err, &typed) || typed.Kind != IssuerInvalidResponse {
		t.Fatalf("missing required activations field error=%#v", err)
	}
}

func TestIssuerActivationAndPortalContractsUseBearerAuthentication(t *testing.T) {
	const license = "rl_live_bearer_secret"
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+license {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		requests = append(requests, r.Method+" "+r.RequestURI)
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"activations":[{"id":"opaque","label":"Mac","first_seen":"2026-01-02T03:04:05Z","current":true}]}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{"url":"https://billing.example.com/session/fresh"}`))
		}
	}))
	defer server.Close()
	client, _ := NewIssuerClient(server.URL, server.Client())
	if _, err := client.Activations(context.Background(), license); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteActivation(context.Background(), license, "opaque/id?# space"); err != nil {
		t.Fatal(err)
	}
	portal, err := client.Portal(context.Background(), license)
	if err != nil || portal.Scheme != "https" {
		t.Fatalf("portal = %v, err=%v", portal, err)
	}
	want := []string{"GET /v1/activations", "DELETE /v1/activations/opaque%2Fid%3F%23%20space", "POST /v1/portal"}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests=%v want=%v", requests, want)
	}
}

func TestRelayEntitlementRefreshPreservesSocketAndClassifiesNoHost(t *testing.T) {
	const tokenValue = "entitlement-refresh-secret"
	statuses := []int{http.StatusNoContent, http.StatusLocked}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/session/session-refresh-123456/entitlement" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Redline-Entitlement") != tokenValue {
			t.Fatal("refresh token was not sent in the header")
		}
		status := statuses[0]
		statuses = statuses[1:]
		w.WriteHeader(status)
	}))
	defer server.Close()
	if err := RefreshRelayEntitlement(context.Background(), server.Client(), server.URL, "session-refresh-123456", NewSecret(tokenValue)); err != nil {
		t.Fatalf("204 refresh: %v", err)
	}
	err := RefreshRelayEntitlement(context.Background(), server.Client(), server.URL, "session-refresh-123456", NewSecret(tokenValue))
	var typed *RelayRefreshError
	if !errors.As(err, &typed) || typed.Kind != RelayRefreshNoHost {
		t.Fatalf("423 refresh error=%#v", err)
	}
	if strings.Contains(err.Error(), tokenValue) {
		t.Fatal("refresh error leaked token")
	}
}

func TestEntitlementCacheSaveContextStopsWhileProcessLockIsBlocked(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-context-session-123456")
	exp := now.Add(EntitlementLifetime).Unix()
	store := NewEntitlementCacheStore(filepath.Join(t.TempDir(), "relay-entitlement.json"))
	if err := store.lock.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer store.lock.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.SaveContext(ctx, CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: CredentialFingerprint("test-license"),
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp, ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked context-aware save error=%v", err)
	}
}

func TestEntitlementCacheIsExactOwnerOnlyAndRejectsSymlinks(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-session-123456789")
	exp := now.Add(EntitlementLifetime).Unix()
	path := filepath.Join(t.TempDir(), "relay-entitlement.json")
	store := NewEntitlementCacheStore(path)
	fingerprint := CredentialFingerprint("test-license")
	want := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode=%v err=%v", info.Mode().Perm(), err)
	}
	got, exists, err := store.Load(sid, fingerprint, now)
	if err != nil || !exists || got.Token.Value() != want.Token.Value() {
		t.Fatalf("load=%#v exists=%v err=%v", got, exists, err)
	}
	if _, _, err := store.Load(SessionSID("wrong-session-123456"), fingerprint, now); err == nil {
		t.Fatal("cache bound to another sid was accepted")
	}
	if _, _, err := store.Load(sid, fingerprint, time.Unix(exp, 0).Add(EntitlementClockSkew)); err == nil {
		t.Fatal("cache beyond the bounded expiry skew was accepted")
	}
	raw, _ := os.ReadFile(path)
	var shape map[string]any
	_ = json.Unmarshal(raw, &shape)
	if len(shape) != 7 || shape["schema_version"] != float64(EntitlementCacheSchemaVersion) || shape["credential_fingerprint"] != fingerprint {
		t.Fatalf("cache schema is not versioned and credential-bound: %v", shape)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(sid, fingerprint, now); err == nil {
		t.Fatal("overpermissive cache was accepted")
	}
	_ = os.Remove(path)
	target := filepath.Join(filepath.Dir(path), "target")
	_ = os.WriteFile(target, raw, 0o600)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(sid, fingerprint, now); err == nil {
		t.Fatal("symlink cache was accepted")
	}
	if err := store.Save(want); err == nil {
		t.Fatal("renewal replaced a symlink cache")
	}
}

func TestEntitlementCacheRejectsReplacementCredentialAndDurableTombstone(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-revocation-session-123456")
	exp := now.Add(EntitlementLifetime).Unix()
	path := filepath.Join(t.TempDir(), "relay-entitlement.json")
	store := NewEntitlementCacheStore(path)
	oldFingerprint := CredentialFingerprint("old-high-entropy-license")
	cached := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: oldFingerprint,
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	}
	if err := store.Save(cached); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.Load(sid, CredentialFingerprint("replacement-license"), now); err == nil || valid {
		t.Fatal("replacement credential trusted old cache authority")
	}
	tombstone := CachedEntitlement{SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: oldFingerprint, Revoked: true}
	if err := store.Save(tombstone); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.Load(sid, oldFingerprint, now); err == nil || valid {
		t.Fatal("durably revoked credential retained cache authority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "old-high-entropy-license") || strings.Contains(string(raw), cached.Token.Value()) {
		t.Fatal("tombstone retained reversible credential or authority token")
	}
}

func TestEntitlementValidationAllowsOnlyBoundedClockSkew(t *testing.T) {
	receivedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sid := SessionSID("session-clock-skew-123456")
	for _, tc := range []struct {
		name string
		exp  time.Time
		ok   bool
	}{
		{"positive skew", receivedAt.Add(EntitlementLifetime + EntitlementClockSkew), true},
		{"positive skew exceeded", receivedAt.Add(EntitlementLifetime + EntitlementClockSkew + time.Second), false},
		{"negative skew", receivedAt.Add(-EntitlementClockSkew + time.Second), true},
		{"negative skew exceeded", receivedAt.Add(-EntitlementClockSkew), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entitlement := Entitlement{Token: NewSecret(testEntitlementToken(t, tc.exp.Unix(), sid, 5)), Exp: tc.exp.Unix(), MaxClients: 5, Seats: 1, SeatsUsed: 1}
			err := ValidateEntitlementAt(entitlement, sid, receivedAt)
			if (err == nil) != tc.ok {
				t.Fatalf("validation error=%v, want ok=%v", err, tc.ok)
			}
		})
	}

	futureReceipt := receivedAt.Add(EntitlementClockSkew)
	cached := CachedEntitlement{SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: CredentialFingerprint("test-license"), Token: NewSecret(testEntitlementToken(t, futureReceipt.Add(time.Hour).Unix(), sid, 5)), Exp: futureReceipt.Add(time.Hour).Unix(), ObtainedAt: futureReceipt.Unix(), SID: sid, MaxClients: 5}
	if !cached.ValidAt(sid, receivedAt) {
		t.Fatal("bounded positive obtained_at skew was rejected")
	}
	cached.ObtainedAt++
	if cached.ValidAt(sid, receivedAt) {
		t.Fatal("obtained_at beyond skew allowance was accepted")
	}
}

func TestCachedEntitlementAuthorityEndsAtSignedExpiration(t *testing.T) {
	receivedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sid := SessionSID("session-raw-expiration-123456")
	exp := receivedAt.Add(time.Hour).Unix()
	cached := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: CredentialFingerprint("test-license"),
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: receivedAt.Unix(), SID: sid, MaxClients: 5,
	}
	if !cached.ValidAt(sid, time.Unix(exp, 0).Add(-time.Nanosecond)) {
		t.Fatal("cache was not valid immediately before signed expiration")
	}
	if cached.ValidAt(sid, time.Unix(exp, 0)) {
		t.Fatal("validation skew extended cache authority beyond signed expiration")
	}
}

func TestRelayEntitlementRefreshRejectsRedirectWithoutForwardingCredential(t *testing.T) {
	const token = "redirect-secret-entitlement"
	arrived := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		arrived <- "request arrived"
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	err := RefreshRelayEntitlement(context.Background(), source.Client(), source.URL, "session-redirect-123456", NewSecret(token))
	var typed *RelayRefreshError
	if !errors.As(err, &typed) || typed.Kind != RelayRefreshRejected {
		t.Fatalf("redirect error=%#v", err)
	}
	select {
	case <-arrived:
		t.Fatal("redirect target received the entitlement request")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRelayEntitlementRefreshRejectsHostileSessionIDsBeforeRequest(t *testing.T) {
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	for _, sessionID := range []string{"../admin-session-1234", "slash/session-1234", "dot%2Fescape-session", ".", "short"} {
		err := RefreshRelayEntitlement(context.Background(), server.Client(), server.URL, sessionID, NewSecret("secret"))
		var typed *RelayRefreshError
		if !errors.As(err, &typed) || typed.Kind != RelayRefreshRejected {
			t.Fatalf("session %q error=%#v", sessionID, err)
		}
	}
	if requests != 0 {
		t.Fatalf("hostile session ids reached network: %d requests", requests)
	}
}
