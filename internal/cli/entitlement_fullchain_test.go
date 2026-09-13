package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/relay"
	core "github.com/jfox/redline/mobile/core"
)

type fullchainLicenseStore struct{}

func (fullchainLicenseStore) Load(context.Context) (string, error)  { return "test-license", nil }
func (fullchainLicenseStore) Replace(context.Context, string) error { return nil }
func (fullchainLicenseStore) Clear(context.Context) error           { return nil }

type fullchainCache struct {
	mu          sync.Mutex
	current     relay.CachedEntitlement
	saveStarted chan struct{}
	releaseSave <-chan struct{}
}

func (c *fullchainCache) Load(sid, fingerprint string, now time.Time) (relay.CachedEntitlement, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.current
	if current.SchemaVersion == 0 {
		current.SchemaVersion = relay.EntitlementCacheSchemaVersion
		current.CredentialFingerprint = fingerprint
	}
	return current, current.ValidAt(sid, now), nil
}
func (c *fullchainCache) SaveContext(ctx context.Context, value relay.CachedEntitlement) error {
	if c.saveStarted != nil {
		close(c.saveStarted)
		select {
		case <-c.releaseSave:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	c.current = value
	c.mu.Unlock()
	return nil
}

type fullchainIssuer struct {
	release <-chan struct{}
	private ed25519.PrivateKey
}

func (i fullchainIssuer) Entitlement(ctx context.Context, _ string, sid string, _ string) (relay.ReceivedEntitlement, error) {
	select {
	case <-ctx.Done():
		return relay.ReceivedEntitlement{}, ctx.Err()
	case <-i.release:
	}
	receivedAt := time.Now().UTC().Truncate(time.Second)
	exp := receivedAt.Add(relay.EntitlementLifetime).Unix()
	issued := relay.Entitlement{Token: fullchainToken(i.private, exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}
	return relay.ReceivedEntitlement{Entitlement: issued, ReceivedAt: receivedAt}, nil
}

type fullchainClaims struct {
	Exp        int64  `json:"exp"`
	SID        string `json:"sid"`
	MaxClients int    `json:"max_clients"`
}

func fullchainToken(private ed25519.PrivateKey, exp int64, sid string, maxClients int) relay.Secret {
	claims, _ := json.Marshal(fullchainClaims{Exp: exp, SID: sid, MaxClients: maxClients})
	signature := ed25519.Sign(private, claims)
	return relay.NewSecret(base64.StdEncoding.EncodeToString(claims) + "." + base64.StdEncoding.EncodeToString(signature))
}

func validateFullchainToken(public ed25519.PublicKey, token, sid string, now time.Time) (fullchainClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fullchainClaims{}, errors.New("token format")
	}
	claimsRaw, err := base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return fullchainClaims{}, errors.New("claims encoding")
	}
	signature, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || !ed25519.Verify(public, claimsRaw, signature) {
		return fullchainClaims{}, errors.New("signature")
	}
	var claims fullchainClaims
	if err := json.Unmarshal(claimsRaw, &claims); err != nil || claims.SID != sid || claims.Exp <= now.Unix() || claims.MaxClients < 1 || claims.MaxClients > 25 {
		return fullchainClaims{}, errors.New("claims")
	}
	return claims, nil
}

type fullchainAuthorityInstall struct {
	claims    fullchainClaims
	token     string
	reconnect bool
}

type fullchainRelayAuthority struct {
	mu            sync.Mutex
	claims        fullchainClaims
	alarmUnix     int64
	generation    uint64
	installations []fullchainAuthorityInstall
}

// install matches the production Worker: every valid signed refresh or host
// reconnect replaces claims and the alarm, even when exp is older.
func (a *fullchainRelayAuthority) install(claims fullchainClaims, token string, reconnect bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims = claims
	a.alarmUnix = claims.Exp
	a.installations = append(a.installations, fullchainAuthorityInstall{claims: claims, token: token, reconnect: reconnect})
	if reconnect {
		a.generation++
	}
}

func (a *fullchainRelayAuthority) snapshot() (fullchainClaims, int64, uint64, []fullchainAuthorityInstall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claims, a.alarmUnix, a.generation, append([]fullchainAuthorityInstall(nil), a.installations...)
}

// TestEntitlementRefreshFullChain joins the controller, supervisor, real
// dialer's internal reconnect loop, TLS relay stand-in, and a blocked forwarded
// request. It deliberately does not require a production Worker deployment.
func TestEntitlementRefreshFullChain(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keypair, err := core.NewDesktopKeypair()
	if err != nil {
		t.Fatal(err)
	}
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer local.Close()

	firstConnected := make(chan struct{})
	requestCompleted := make(chan error, 1)
	handshakeTokens := make(chan string, 3)
	refreshTokens := make(chan string, 1)
	var connections int
	var connectionMu sync.Mutex
	authority := &fullchainRelayAuthority{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/session/session-fullchain-1234/entitlement", func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Redline-Entitlement")
		claims, validationErr := validateFullchainToken(public, token, relay.SessionSID("session-fullchain-1234"), time.Now())
		if validationErr != nil {
			http.Error(w, "not entitled", http.StatusPaymentRequired)
			return
		}
		authority.install(claims, token, false)
		refreshTokens <- token
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/session/session-fullchain-1234", func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Redline-Entitlement")
		claims, validationErr := validateFullchainToken(public, token, relay.SessionSID("session-fullchain-1234"), time.Now())
		if validationErr != nil {
			http.Error(w, "not entitled", http.StatusPaymentRequired)
			return
		}
		authority.install(claims, token, true)
		handshakeTokens <- token
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connectionMu.Lock()
		connections++
		connection := connections
		connectionMu.Unlock()
		if connection != 1 {
			_, _, _ = conn.Read(context.Background())
			return
		}
		close(firstConnected)
		phone, err := core.NewInitiatorSession(core.DesktopPublicKey(keypair))
		if err != nil {
			requestCompleted <- err
			return
		}
		channel := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
		first, _ := phone.StartHandshake()
		if err := conn.Write(r.Context(), websocket.MessageBinary, append(channel[:], first...)); err != nil {
			requestCompleted <- err
			return
		}
		_, reply, err := conn.Read(r.Context())
		if err != nil {
			requestCompleted <- err
			return
		}
		if len(reply) < len(channel) || string(reply[:len(channel)]) != string(channel[:]) {
			requestCompleted <- errors.New("handshake reply used wrong relay channel")
			return
		}
		if err := phone.FinishHandshake(reply[len(channel):]); err != nil {
			requestCompleted <- err
			return
		}
		request, _ := relay.EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
		sealed, _ := phone.Seal(request)
		if err := conn.Write(r.Context(), websocket.MessageBinary, append(channel[:], sealed...)); err != nil {
			requestCompleted <- err
			return
		}
		_, response, err := conn.Read(r.Context())
		if err == nil {
			if len(response) < len(channel) || string(response[:len(channel)]) != string(channel[:]) {
				requestCompleted <- errors.New("response used wrong relay channel")
				return
			}
			opened, openErr := phone.Open(response[len(channel):])
			if openErr == nil {
				decoded, decodeErr := relay.DecodeResponse(opened)
				if decodeErr != nil {
					err = decodeErr
				} else if decoded.Status != http.StatusOK || !strings.Contains(string(decoded.Body), `"ok":true`) {
					err = fmt.Errorf("unexpected forwarded response: status=%d body=%q", decoded.Status, decoded.Body)
				}
			} else {
				err = openErr
			}
		}
		requestCompleted <- err
		// A malformed envelope with no complete channel forces a fresh host
		// connection after the live refresh.
		_ = conn.Write(context.Background(), websocket.MessageBinary, []byte("bad"))
	})
	tlsRelay := httptest.NewTLSServer(mux)
	defer tlsRelay.Close()

	now := time.Now().UTC().Truncate(time.Second)
	sessionID := "session-fullchain-1234"
	sid := relay.SessionSID(sessionID)
	oldExp := now.Add(2 * time.Hour).Unix()
	oldToken := fullchainToken(private, oldExp, sid, 5)
	initial := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, URL: tlsRelay.URL, SessionID: sessionID},
		Readiness:         config.RelayReadinessActive, Dial: true, EntitlementToken: config.NewRelayEntitlementToken(oldToken.Value()),
		RenewsAt: now.Add(30 * time.Minute), ExpiresAt: time.Unix(oldExp, 0), MaxClients: 5,
	}
	coordinator := config.NewRelayCoordinator(initial)
	supervisor := newRelaySupervisor(coordinator, func(snapshot config.ResolvedRelay, tokenSource func() string) (relayDialerRun, error) {
		dialer := relay.NewDialer(relay.DialerOptions{
			RelayURL: snapshot.URL, SessionID: snapshot.SessionID, Keypair: keypair,
			Forwarder: relay.NewForwarder(local.URL, local.Client()), EntitlementTokenSource: tokenSource, HTTPClient: tlsRelay.Client(),
		})
		return dialer.Run, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Start the initial dialer but deliberately pause subscription consumption.
	// Internal reconnects must still read coordinator.Current synchronously.
	if err := supervisor.apply(ctx, supervisor.Initial()); err != nil {
		t.Fatal(err)
	}
	if got := <-handshakeTokens; got != oldToken.Value() {
		t.Fatalf("initial handshake token mismatch")
	}
	<-firstConnected

	releaseIssuer := make(chan struct{})
	saveStarted := make(chan struct{})
	releaseSave := make(chan struct{})
	cache := &fullchainCache{
		current:     relay.CachedEntitlement{Token: oldToken, Exp: oldExp, ObtainedAt: now.Add(-time.Hour).Unix(), SID: sid, MaxClients: 5},
		saveStarted: saveStarted, releaseSave: releaseSave,
	}
	controller := config.NewEntitlementController(config.EntitlementControllerOptions{
		Coordinator: coordinator, Initial: initial, Licenses: fullchainLicenseStore{}, Issuer: fullchainIssuer{release: releaseIssuer, private: private},
		Cache: cache, RelayHTTP: tlsRelay.Client(),
	})
	controllerDone := make(chan struct{})
	go func() { controller.Run(ctx); close(controllerDone) }()
	<-requestStarted
	close(releaseIssuer)
	newToken := <-refreshTokens
	if newToken == "" || newToken == oldToken.Value() {
		t.Fatal("relay refresh did not receive a fresh token")
	}
	newClaims, validationErr := validateFullchainToken(public, newToken, sid, time.Now())
	if validationErr != nil {
		t.Fatalf("refreshed signed token was invalid: %v", validationErr)
	}
	installed, alarm, generation, installations := authority.snapshot()
	if installed != newClaims || alarm != newClaims.Exp || generation != 1 || alarm <= oldExp {
		t.Fatalf("refresh did not advance installed claims/alarm: claims=%#v alarm=%d generation=%d", installed, alarm, generation)
	}
	for name, token := range map[string]string{
		"bad signature": newToken + "corrupt",
		"wrong sid":     fullchainToken(private, newClaims.Exp, relay.SessionSID("different-session-123456"), 5).Value(),
		"invalid max":   fullchainToken(private, newClaims.Exp, sid, 26).Value(),
	} {
		req, requestErr := http.NewRequest(http.MethodGet, tlsRelay.URL+"/v1/session/"+sessionID, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("X-Redline-Entitlement", token)
		resp, requestErr := tlsRelay.Client().Do(req)
		if requestErr != nil {
			t.Fatalf("%s request: %v", name, requestErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("%s token status=%d, want 402", name, resp.StatusCode)
		}
	}
	deadline := time.Now().Add(time.Second)
	updated := coordinator.Current()
	for updated.EntitlementToken.Value() != newToken && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		updated = coordinator.Current()
	}
	if !updated.CanDial() || updated.EntitlementToken.Value() != newToken || updated.RenewsAt.Before(initial.RenewsAt) {
		t.Fatalf("renewal moved runtime authority backward: %#v", updated)
	}
	connectionMu.Lock()
	if connections != 1 {
		t.Fatalf("204 refresh restarted live socket: connections=%d", connections)
	}
	connectionMu.Unlock()
	select {
	case <-saveStarted:
		// The token source was updated even though durable persistence is still
		// blocked, which is the reconnect race this contract guards.
	case <-time.After(time.Second):
		t.Fatal("controller did not reach blocked persistence")
	}
	close(releaseSave)
	close(releaseRequest)
	if err := <-requestCompleted; err != nil {
		t.Fatalf("blocked request did not survive refresh: %v", err)
	}
	select {
	case got := <-handshakeTokens:
		if got != newToken {
			t.Fatal("internal reconnect did not use newest runtime token")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forced internal reconnect did not reach relay")
	}
	installed, alarm, generation, installations = authority.snapshot()
	if installed != newClaims || alarm != newClaims.Exp || generation != 2 {
		t.Fatalf("reconnect installed unexpected authority: claims=%#v alarm=%d generation=%d", installed, alarm, generation)
	}
	if len(installations) != 3 {
		t.Fatalf("relay installations=%d want initial, refresh, reconnect", len(installations))
	}
	wantTokens := []string{oldToken.Value(), newToken, newToken}
	wantExps := []int64{oldExp, newClaims.Exp, newClaims.Exp}
	for i, installation := range installations {
		if installation.token != wantTokens[i] || installation.claims.Exp != wantExps[i] {
			t.Fatalf("installation %d authority was stale: token match=%v exp=%d want=%d", i, installation.token == wantTokens[i], installation.claims.Exp, wantExps[i])
		}
	}
	cancel()
	<-controllerDone
	supervisor.stopActive()
	supervisor.Close()
}
