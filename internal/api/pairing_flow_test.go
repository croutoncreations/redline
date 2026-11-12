package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/store"
)

func TestPairingEndpointMintsExactlyOnceAfterPreparation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "redline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := config.Config{
		Database: "unused.db",
		API:      config.API{TrustedHosts: []string{"macbook.example.ts.net"}},
		APIToken: "test-token-that-is-at-least-thirty-two-characters",
	}
	runtime := config.NewRelayCoordinator(config.ResolvedRelay{
		RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff},
		Readiness:         config.RelayReadinessOff,
	})
	server := NewServerWithRelayRuntime(cfg, database, time.Now, runtime)
	mints := 0
	server.mintPairingToken = func() (string, error) {
		mints++
		return "one-minted-token", nil
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7436/v1/pairing", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Token      string `json:"pairing_token"`
		PairingURL string `json:"pairing_url"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if mints != 1 || response.Token != "one-minted-token" || !strings.Contains(response.PairingURL, "pairing_token=one-minted-token") {
		t.Fatalf("mints=%d response=%#v", mints, response)
	}
	if len(server.pairing) != 1 {
		t.Fatalf("stored pairing tokens=%d, want exactly the returned token", len(server.pairing))
	}
	if _, ok := server.pairing[response.Token]; !ok {
		t.Fatalf("returned token was not the one stored: %#v", server.pairing)
	}
}
