package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func (c *fullchainCache) Load(sid string, now time.Time) (relay.CachedEntitlement, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current, c.current.ValidAt(sid, now), nil
}
func (c *fullchainCache) Save(value relay.CachedEntitlement) error {
	if c.saveStarted != nil {
		close(c.saveStarted)
		<-c.releaseSave
	}
	c.mu.Lock()
	c.current = value
	c.mu.Unlock()
	return nil
}

type fullchainIssuer struct {
	release <-chan struct{}
}

func (i fullchainIssuer) Entitlement(ctx context.Context, _ string, sid string, _ string, now time.Time) (relay.Entitlement, error) {
	select {
	case <-ctx.Done():
		return relay.Entitlement{}, ctx.Err()
	case <-i.release:
	}
	exp := now.Add(relay.EntitlementLifetime).Unix()
	return relay.Entitlement{Token: fullchainToken(exp, sid, 5), Exp: exp, MaxClients: 5, Seats: 1, SeatsUsed: 1}, nil
}

func fullchainToken(exp int64, sid string, maxClients int) relay.Secret {
	claims, _ := json.Marshal(map[string]any{"exp": exp, "sid": sid, "max_clients": maxClients})
	return relay.NewSecret(base64.StdEncoding.EncodeToString(claims) + "." + base64.StdEncoding.EncodeToString(make([]byte, 64)))
}

// TestEntitlementRefreshFullChain joins the controller, supervisor, real
// dialer's internal reconnect loop, TLS relay stand-in, and a blocked forwarded
// request. It deliberately does not require a production Worker deployment.
func TestEntitlementRefreshFullChain(t *testing.T) {
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

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/session/session-fullchain-1234/entitlement", func(w http.ResponseWriter, r *http.Request) {
		refreshTokens <- r.Header.Get("X-Redline-Entitlement")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/session/session-fullchain-1234", func(w http.ResponseWriter, r *http.Request) {
		handshakeTokens <- r.Header.Get("X-Redline-Entitlement")
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
		first, _ := phone.StartHandshake()
		if err := conn.Write(r.Context(), websocket.MessageBinary, first); err != nil {
			requestCompleted <- err
			return
		}
		_, reply, err := conn.Read(r.Context())
		if err != nil {
			requestCompleted <- err
			return
		}
		if err := phone.FinishHandshake(reply); err != nil {
			requestCompleted <- err
			return
		}
		request, _ := relay.EncodeRequestParts(http.MethodGet, "/v1/dashboard", nil, nil)
		sealed, _ := phone.Seal(request)
		if err := conn.Write(r.Context(), websocket.MessageBinary, sealed); err != nil {
			requestCompleted <- err
			return
		}
		_, response, err := conn.Read(r.Context())
		if err == nil {
			opened, openErr := phone.Open(response)
			if openErr == nil {
				decoded, decodeErr := relay.DecodeResponse(opened)
				if decodeErr != nil || decoded.Status != http.StatusOK || !strings.Contains(string(decoded.Body), `"ok":true`) {
					err = decodeErr
				}
			} else {
				err = openErr
			}
		}
		requestCompleted <- err
		// Force the dialer's prompt bad-frame reconnect after the live refresh.
		_ = conn.Write(context.Background(), websocket.MessageBinary, []byte("force reconnect"))
	})
	tlsRelay := httptest.NewTLSServer(mux)
	defer tlsRelay.Close()

	now := time.Now().UTC().Truncate(time.Second)
	sessionID := "session-fullchain-1234"
	sid := relay.SessionSID(sessionID)
	oldExp := now.Add(2 * time.Hour).Unix()
	oldToken := fullchainToken(oldExp, sid, 5)
	initial := config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeHosted, URL: tlsRelay.URL, SessionID: sessionID},
		Readiness:         config.RelayReadinessActive, Dial: true, EntitlementToken: config.NewRelayEntitlementToken(oldToken.Value()),
		RenewsAt: now.Add(30 * time.Minute), MaxClients: 5,
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
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- supervisor.Run(ctx) }()
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
		Coordinator: coordinator, Initial: initial, Licenses: fullchainLicenseStore{}, Issuer: fullchainIssuer{release: releaseIssuer},
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
	cancel()
	<-controllerDone
	if err := <-supervisorDone; err != nil {
		t.Fatal(err)
	}
}
