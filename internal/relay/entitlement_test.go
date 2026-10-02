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
	if err := client.DeleteActivation(context.Background(), license, "opaque_id-123"); err != nil {
		t.Fatal(err)
	}
	portal, err := client.Portal(context.Background(), license)
	if err != nil || portal.Scheme != "https" {
		t.Fatalf("portal = %v, err=%v", portal, err)
	}
	want := []string{"GET /v1/activations", "DELETE /v1/activations/opaque_id-123", "POST /v1/portal"}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests=%v want=%v", requests, want)
	}
}

func TestIssuerRejectsHostileActivationIDsBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client, _ := NewIssuerClient(server.URL, server.Client())
	for _, id := range []string{".", "..", "slash/id", "percent%2Fid", "query?id", "fragment#id", "space id"} {
		if err := client.DeleteActivation(context.Background(), "license", id); err == nil {
			t.Errorf("hostile activation id %q was accepted", id)
		}
	}
	if calls != 0 {
		t.Fatalf("hostile ids reached issuer: calls=%d", calls)
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

func TestEntitlementRevocationStoreSerializesAppendBeforeGuardedCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-entitlement-revocation.json")
	guardStore := NewEntitlementRevocationStore(path)
	appendStore := NewEntitlementRevocationStore(path)
	fingerprint := CredentialFingerprint("guarded-commit-license")
	token := NewSecret("guarded-commit-token")
	beforeCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	type commitResult struct {
		committed      bool
		callbackCalled bool
		err            error
	}
	result := make(chan commitResult, 1)

	go func() {
		close(beforeCommit)
		<-releaseCommit
		callbackCalled := false
		_, committed, err := guardStore.CommitIfUnrevoked(context.Background(), fingerprint, EntitlementTokenHash(token), func() bool {
			callbackCalled = true
			return true
		})
		result <- commitResult{committed: committed, callbackCalled: callbackCalled, err: err}
	}()
	<-beforeCommit
	if err := appendStore.SaveContext(context.Background(), NewEntitlementRevocation(fingerprint, EntitlementTokenHash(token))); err != nil {
		t.Fatal(err)
	}
	close(releaseCommit)
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.callbackCalled || got.committed {
		t.Fatal("guarded commit accepted an exact revocation appended before lock acquisition")
	}
}

func TestEntitlementRevocationHashesAreExactMonotonicAndSecretFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-entitlement-revocation.json")
	store := NewEntitlementRevocationStore(path)
	fingerprint := CredentialFingerprint("hash-revocation-license")
	firstToken := NewSecret("first-plaintext-entitlement")
	secondToken := NewSecret("second-plaintext-entitlement")
	first := NewEntitlementRevocation(fingerprint, EntitlementTokenHash(firstToken))
	second := NewEntitlementRevocation(fingerprint, EntitlementTokenHash(secondToken))
	var wg sync.WaitGroup
	for _, marker := range []EntitlementRevocation{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.SaveContext(context.Background(), marker); err != nil {
				t.Errorf("save marker: %v", err)
			}
		}()
	}
	wg.Wait()
	got, exists, err := store.Load()
	if err != nil || !exists || len(got.Revocations[fingerprint]) != 2 || !got.Revokes(fingerprint, firstToken) || !got.Revokes(fingerprint, secondToken) {
		t.Fatalf("merged marker=%#v exists=%v err=%v", got, exists, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), firstToken.Value()) || strings.Contains(string(raw), secondToken.Value()) {
		t.Fatalf("marker leaked token plaintext: %s", raw)
	}
}

func TestEntitlementRevocationLedgerPreservesDifferentCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-entitlement-revocation.json")
	store := NewEntitlementRevocationStore(path)
	firstFingerprint := CredentialFingerprint("first-ledger-license")
	secondFingerprint := CredentialFingerprint("second-ledger-license")
	firstToken := NewSecret("first-ledger-token")
	secondToken := NewSecret("second-ledger-token")
	thirdToken := NewSecret("third-ledger-token")
	if err := store.SaveContext(context.Background(), NewEntitlementRevocation(firstFingerprint, EntitlementTokenHash(firstToken))); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(context.Background(), NewEntitlementRevocation(secondFingerprint, EntitlementTokenHash(secondToken))); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(context.Background(), NewEntitlementRevocation(firstFingerprint, EntitlementTokenHash(thirdToken))); err != nil {
		t.Fatal(err)
	}
	got, exists, err := store.Load()
	if err != nil || !exists || !got.Revokes(firstFingerprint, firstToken) || !got.Revokes(secondFingerprint, secondToken) || !got.Revokes(firstFingerprint, thirdToken) {
		t.Fatalf("ledger=%#v exists=%v err=%v", got, exists, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), firstToken.Value()) || strings.Contains(string(raw), secondToken.Value()) || strings.Contains(string(raw), thirdToken.Value()) {
		t.Fatalf("ledger leaked token plaintext: %s", raw)
	}
}

func TestEntitlementCacheRejectsReplacementCredential(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-revocation-session-123456")
	exp := now.Add(EntitlementLifetime).Unix()
	store := NewEntitlementCacheStore(filepath.Join(t.TempDir(), "relay-entitlement.json"))
	fingerprint := CredentialFingerprint("old-high-entropy-license")
	cached := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	}
	if err := store.Save(cached); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.Load(sid, CredentialFingerprint("replacement-license"), now); err == nil || valid {
		t.Fatal("replacement credential trusted old cache authority")
	}
}

func TestExactRevocationRejectsOldSaveAndAllowsDistinctAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-monotonic-session-123456")
	cachePath := filepath.Join(t.TempDir(), "relay-entitlement.json")
	cache := NewEntitlementCacheStore(cachePath)
	markers := NewEntitlementRevocationStore(DefaultEntitlementRevocationPath(cachePath))
	fingerprint := CredentialFingerprint("monotonic-high-entropy-license")
	authority := func(obtainedAt time.Time) CachedEntitlement {
		exp := obtainedAt.Add(time.Hour).Unix()
		return CachedEntitlement{
			SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
			Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
			ObtainedAt: obtainedAt.Unix(), SID: sid, MaxClients: 5,
		}
	}
	old := authority(now)
	marker := NewEntitlementRevocation(fingerprint, EntitlementTokenHash(old.Token))
	if err := markers.SaveContext(context.Background(), marker); err != nil {
		t.Fatal(err)
	}
	if err := cache.Save(old); err != nil {
		t.Fatal(err)
	}
	loaded, valid, err := cache.Load(sid, fingerprint, now)
	if err != nil || !valid || !marker.Revokes(fingerprint, loaded.Token) {
		t.Fatalf("old cache candidate=%#v valid=%v err=%v", loaded, valid, err)
	}
	newer := authority(now.Add(time.Second))
	if marker.Revokes(fingerprint, newer.Token) {
		t.Fatal("distinct relay-accepted authority was revoked")
	}
	if err := cache.Save(newer); err != nil {
		t.Fatal(err)
	}
}

func TestEntitlementCacheVisibleAfterDirectorySyncUncertaintyIsUsable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("directory-sync-session-123456")
	path := filepath.Join(t.TempDir(), "relay-entitlement.json")
	fingerprint := CredentialFingerprint("directory-sync-license")
	store := NewEntitlementCacheStore(path)
	exp := now.Add(time.Hour).Unix()
	accepted := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: now.Unix(), SID: sid, MaxClients: 5,
	}
	originalSync := entitlementCacheDirectorySync
	defer func() { entitlementCacheDirectorySync = originalSync }()
	entitlementCacheDirectorySync = func(*os.File) error { return errors.New("injected directory sync failure") }
	if err := store.Save(accepted); err == nil || !strings.Contains(err.Error(), "sync entitlement cache directory") {
		t.Fatalf("cache save error=%v", err)
	}
	entitlementCacheDirectorySync = originalSync
	restarted := NewEntitlementCacheStore(path)
	got, valid, err := restarted.Load(sid, fingerprint, now)
	if err != nil || !valid || got.Token.Value() != accepted.Token.Value() {
		t.Fatalf("visible relay-accepted cache=%#v valid=%v err=%v", got, valid, err)
	}
}

func TestEntitlementCacheSchemaV2AuthorityIsReplaceableLegacy(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-v2-upgrade-session-123456")
	fingerprint := CredentialFingerprint("v2-upgrade-license")
	legacyExp := now.Add(time.Hour).Unix()
	legacy := fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"token":%q,"exp":%d,"obtained_at":%d,"sid":%q,"max_clients":5}`, fingerprint, testEntitlementToken(t, legacyExp, sid, 5), legacyExp, now.Unix(), sid) + "\n"

	for _, tc := range []struct {
		name     string
		incoming CachedEntitlement
	}{
		{
			name: "v3 authority",
			incoming: CachedEntitlement{
				SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
				Token: NewSecret(testEntitlementToken(t, now.Add(2*time.Hour).Unix(), sid, 5)),
				Exp:   now.Add(2 * time.Hour).Unix(), ObtainedAt: now.Add(time.Minute).Unix(), SID: sid, MaxClients: 5,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay-entitlement.json")
			if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewEntitlementCacheStore(path)
			if _, valid, err := store.Load(sid, fingerprint, now); err == nil || valid {
				t.Fatal("schema-v2 cache was accepted as authority")
			}
			if err := store.SaveContext(context.Background(), tc.incoming); err != nil {
				t.Fatalf("replace schema-v2 cache: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeEntitlementCache(raw)
			if err != nil || decoded.SchemaVersion != EntitlementCacheSchemaVersion || decoded.Token.Value() != tc.incoming.Token.Value() {
				t.Fatalf("replacement=%#v err=%v", decoded, err)
			}
		})
	}
}

func TestEntitlementCacheRejectsMalformedOrFutureCacheReplacement(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	sid := SessionSID("cache-replacement-reject-session")
	fingerprint := CredentialFingerprint("replacement-reject-license")
	incomingExp := now.Add(2 * time.Hour).Unix()
	incoming := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: NewSecret(testEntitlementToken(t, incomingExp, sid, 5)), Exp: incomingExp,
		ObtainedAt: now.Add(time.Minute).Unix(), SID: sid, MaxClients: 5,
	}
	legacyExp := now.Add(time.Hour).Unix()
	legacyToken := testEntitlementToken(t, legacyExp, sid, 5)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed v2", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"token":"bad","exp":1,"obtained_at":1,"sid":%q}`, fingerprint, sid) + "\n"},
		{"v2 explicit null token", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"token":null,"exp":1,"obtained_at":1,"sid":%q,"max_clients":5}`, fingerprint, sid) + "\n"},
		{"v2 explicit null revoked", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"revoked":null,"token":"bad","exp":1,"obtained_at":1,"sid":%q,"max_clients":5}`, fingerprint, sid) + "\n"},
		{"v2 omitted token", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"exp":1,"obtained_at":1,"sid":%q,"max_clients":5}`, fingerprint, sid) + "\n"},
		{"v2 explicit false revoked", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"revoked":false,"token":"bad","exp":1,"obtained_at":1,"sid":%q,"max_clients":5}`, fingerprint, sid) + "\n"},
		{"v2 duplicate token", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"token":"bad","token":%q,"exp":%d,"obtained_at":%d,"sid":%q,"max_clients":5}`, fingerprint, legacyToken, legacyExp, now.Unix(), sid) + "\n"},
		{"v2 duplicate revoked", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"revoked":false,"revoked":true}`, fingerprint) + "\n"},
		{"v2 duplicate other field", fmt.Sprintf(`{"schema_version":2,"credential_fingerprint":%q,"token":%q,"exp":%d,"obtained_at":%d,"sid":%q,"max_clients":4,"max_clients":5}`, fingerprint, legacyToken, legacyExp, now.Unix(), sid) + "\n"},
		{"future schema", fmt.Sprintf(`{"schema_version":4,"credential_fingerprint":%q,"token":%q,"exp":%d,"obtained_at":%d,"sid":%q,"max_clients":5}`, fingerprint, legacyToken, legacyExp, now.Unix(), sid) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay-entitlement.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewEntitlementCacheStore(path)
			if err := store.SaveContext(context.Background(), incoming); err == nil {
				t.Fatal("unsafe existing cache was replaced")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.raw {
				t.Fatalf("unsafe cache changed to %q, err=%v", got, err)
			}
		})
	}
}

func TestEntitlementRevocationStorePersistsClosedMarkerBesideCache(t *testing.T) {
	markerPath := DefaultEntitlementRevocationPath(filepath.Join(t.TempDir(), "relay-entitlement.json"))
	fingerprint := CredentialFingerprint("revoked-license")
	token := NewSecret("revoked-token-plaintext")
	store := NewEntitlementRevocationStore(markerPath)
	marker := NewEntitlementRevocation(fingerprint, EntitlementTokenHash(token))
	if err := store.SaveContext(context.Background(), marker); err != nil {
		t.Fatal(err)
	}
	got, exists, err := store.Load()
	if err != nil || !exists || !got.Revokes(fingerprint, token) {
		t.Fatalf("marker=%#v exists=%v err=%v", got, exists, err)
	}
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape) != 2 || shape["schema_version"] != float64(EntitlementRevocationSchemaVersion) || shape["revocations"] == nil {
		t.Fatalf("marker schema=%v", shape)
	}
	if strings.Contains(string(raw), token.Value()) || strings.Contains(string(raw), "revoked-license") {
		t.Fatalf("marker retained authority or credential: %s", raw)
	}
	info, err := os.Stat(markerPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestEntitlementRevocationStoreUsesIndependentLockFromCache(t *testing.T) {
	directory := t.TempDir()
	cache := NewEntitlementCacheStore(filepath.Join(directory, "relay-entitlement.json"))
	markerStore := NewEntitlementRevocationStore(DefaultEntitlementRevocationPath(cache.path))
	if err := cache.lock.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cache.lock.release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	marker := NewEntitlementRevocation(
		CredentialFingerprint("independent-lock-license"),
		EntitlementTokenHash(NewSecret("independent-token")),
	)
	if err := markerStore.SaveContext(ctx, marker); err != nil {
		t.Fatalf("cache lock delayed independent marker: %v", err)
	}
}

func TestEntitlementRevocationStoreRejectsNonCanonicalShapes(t *testing.T) {
	fingerprint := CredentialFingerprint("strict-marker-license")
	hash := EntitlementTokenHash(NewSecret("strict-token"))
	for _, raw := range []string{
		`{"schema_version":3,"revocations":null}`,
		`{"schema_version":3}`,
		fmt.Sprintf(`{"schema_version":2,"revocations":{%q:[%q]}}`, fingerprint, hash),
		fmt.Sprintf(`{"schema_version":3,"revocations":{%q:[%q]},"token":"x"}`, fingerprint, hash),
		fmt.Sprintf(`{"schema_version":3,"revocations":{%q:[%q,%q]}}`, fingerprint, hash, hash),
		fmt.Sprintf(`{"schema_version":3,"revocations":{%q:[%q,%q]}}`, fingerprint, hash, strings.Repeat("0", 64)),
		fmt.Sprintf(`{"schema_version":3,"schema_version":3,"revocations":{%q:[%q]}}`, fingerprint, hash),
		fmt.Sprintf(`{"schema_version":3,"revocations":{%q:[%q],%q:[%q]}}`, fingerprint, hash, fingerprint, hash),
		fmt.Sprintf(`{"schema_version":3,"revocations":{%q:null}}`, fingerprint),
	} {
		path := filepath.Join(t.TempDir(), "relay-entitlement-revocation.json")
		if err := os.WriteFile(path, []byte(raw+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, exists, err := NewEntitlementRevocationStore(path).Load(); err == nil || exists {
			t.Fatalf("unsafe marker accepted: %s", raw)
		}
	}
}

func TestEntitlementCacheRevocationCandidateIncludesFutureDatedAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	obtainedAt := now.Add(EntitlementClockSkew + time.Minute)
	exp := obtainedAt.Add(time.Hour).Unix()
	sid := SessionSID("future-candidate-session-123456")
	fingerprint := CredentialFingerprint("future-candidate-license")
	store := NewEntitlementCacheStore(filepath.Join(t.TempDir(), "relay-entitlement.json"))
	cached := CachedEntitlement{
		SchemaVersion: EntitlementCacheSchemaVersion, CredentialFingerprint: fingerprint,
		Token: NewSecret(testEntitlementToken(t, exp, sid, 5)), Exp: exp,
		ObtainedAt: obtainedAt.Unix(), SID: sid, MaxClients: 5,
	}
	if err := store.Save(cached); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := store.Load(sid, fingerprint, now); err == nil || valid {
		t.Fatal("future-dated cache was published")
	}
	candidate, exists, err := store.LoadRevocationCandidate(fingerprint)
	if err != nil || !exists || candidate.Token.Value() != cached.Token.Value() {
		t.Fatalf("revocation candidate=%#v exists=%v err=%v", candidate, exists, err)
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
