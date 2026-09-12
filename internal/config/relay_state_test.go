package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jfox/redline/internal/config"
)

type fakeLicenseStore struct {
	mu    sync.Mutex
	value string
	loads int
}

func (s *fakeLicenseStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.value == "" {
		return "", config.ErrLicenseNotFound
	}
	return s.value, nil
}

func (s *fakeLicenseStore) Replace(_ context.Context, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}

func (s *fakeLicenseStore) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = ""
	return nil
}

func (s *fakeLicenseStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func TestRelayStatePathSitsBesideIdentity(t *testing.T) {
	got := config.DefaultRelayStatePath("/private/redline/custom-identity.json", "/ignored/redline.db")
	if got != "/private/redline/relay-state.json" {
		t.Fatalf("path = %q", got)
	}
	got = config.DefaultRelayStatePath("", "/var/lib/redline/redline.db")
	if got != "/var/lib/redline/relay-state.json" {
		t.Fatalf("default path = %q", got)
	}
}

func TestRelayStateStoreWritesOnlyManagedNonSecretFieldsAt0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "relay-state.json")
	store := config.NewRelayStateStore(path)
	state := config.RelayManagedState{
		Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL,
		IssuerURL: config.DefaultIssuerURL, Label: "work mac",
		SessionID: "session-abcdefghij0123",
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "license") || strings.Contains(string(raw), "entitlement") {
		t.Fatalf("secret-shaped field serialized: %s", raw)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	want := []string{"mode", "url", "issuer_url", "label", "session_id"}
	if len(document) != len(want) {
		t.Fatalf("keys = %#v", document)
	}
	for _, key := range want {
		if _, ok := document[key]; !ok {
			t.Fatalf("missing %q in %#v", key, document)
		}
	}
}

func TestRelayStateStoreUpdateIsReadModifyWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	original := config.RelayManagedState{
		Mode: config.RelayModeSelfHosted, URL: "https://relay.example.com",
		SessionID: "session-abcdefghij0123",
	}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(func(current config.RelayManagedState, exists bool) (config.RelayManagedState, error) {
		if !exists || current.SessionID != original.SessionID {
			t.Fatalf("current = %#v exists=%v", current, exists)
		}
		current.Label = "desk"
		return current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := store.Load()
	if err != nil || !exists || loaded != updated || loaded.Label != "desk" {
		t.Fatalf("loaded=%#v updated=%#v exists=%v err=%v", loaded, updated, exists, err)
	}
}

func TestRelayStateLoadFailsClosed(t *testing.T) {
	valid := `{"mode":"hosted","url":"https://redline-relay.croutoncreations.com","issuer_url":"https://redline.croutoncreations.com/api","label":"","session_id":"session-abcdefghij0123"}`
	for name, contents := range map[string]string{
		"corrupt":       `{`,
		"unknown field": strings.TrimSuffix(valid, "}") + `,"license_key":"never"}`,
		"bad mode":      strings.Replace(valid, `"hosted"`, `"sometimes"`, 1),
		"unsafe relay":  strings.Replace(valid, `https://redline-relay.croutoncreations.com`, `http://relay.example.com`, 1),
		"bad session":   strings.Replace(valid, `session-abcdefghij0123`, `short`, 1),
		"trailing json": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay-state.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := config.NewRelayStateStore(path).Load(); err == nil {
				t.Fatalf("accepted %s", contents)
			}
		})
	}

	t.Run("overpermissive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "relay-state.json")
		if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := config.NewRelayStateStore(path).Load(); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRelayResolutionPrecedenceAndReadiness(t *testing.T) {
	tests := []struct {
		name       string
		managed    *config.RelayManagedState
		bootstrap  config.Relay
		license    string
		wantMode   config.RelayMode
		wantURL    string
		wantStatus config.RelayReadiness
		wantDial   bool
		wantLoads  int
	}{
		{name: "yaml off", wantMode: config.RelayModeOff, wantStatus: config.RelayReadinessOff},
		{name: "yaml hosted defaults", bootstrap: config.Relay{Enabled: true}, wantMode: config.RelayModeHosted, wantURL: config.DefaultHostedRelayURL, wantStatus: config.RelayReadinessNeedsLicense, wantLoads: 1},
		{name: "yaml self hosted", bootstrap: config.Relay{Enabled: true, URL: "https://relay.example.com", EntitlementToken: "deprecated-secret-must-be-ignored"}, wantMode: config.RelayModeSelfHosted, wantURL: "https://relay.example.com", wantStatus: config.RelayReadinessSelfHosted, wantDial: true},
		{name: "managed off wins over yaml", managed: &config.RelayManagedState{Mode: config.RelayModeOff}, bootstrap: config.Relay{Enabled: true, URL: "https://bootstrap.example.com"}, wantMode: config.RelayModeOff, wantStatus: config.RelayReadinessOff},
		{name: "managed self hosted wins", managed: &config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "https://managed.example.com", SessionID: "session-abcdefghij0123"}, bootstrap: config.Relay{Enabled: true, URL: "https://bootstrap.example.com"}, wantMode: config.RelayModeSelfHosted, wantURL: "https://managed.example.com", wantStatus: config.RelayReadinessSelfHosted, wantDial: true},
		{name: "hosted with key is configured but renewal is phase 2.2", managed: &config.RelayManagedState{Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL, IssuerURL: config.DefaultIssuerURL, SessionID: "session-abcdefghij0123"}, license: "rl_test_not_real", wantMode: config.RelayModeHosted, wantURL: config.DefaultHostedRelayURL, wantStatus: config.RelayReadinessHostedConfigured, wantLoads: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay-state.json")
			store := config.NewRelayStateStore(path)
			if tt.managed != nil {
				if err := store.Save(*tt.managed); err != nil {
					t.Fatal(err)
				}
			}
			licenses := &fakeLicenseStore{value: tt.license}
			got, err := config.NewRelayResolver(store, licenses).Resolve(context.Background(), tt.bootstrap)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != tt.wantMode || got.URL != tt.wantURL || got.Readiness != tt.wantStatus || got.Dial != tt.wantDial {
				t.Fatalf("resolved = %#v", got)
			}
			if got.SessionID == "" && got.Mode != config.RelayModeOff {
				t.Fatal("enabled mode has no session id")
			}
			if licenses.loadCount() != tt.wantLoads {
				t.Fatalf("license loads = %d, want %d", licenses.loadCount(), tt.wantLoads)
			}
		})
	}
}

func TestYAMLNeverAcceptsALicenseKey(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", `active_policy: standard
relay:
  enabled: true
  license_key: rl_test_must_not_parse`, 1)
	_, err := config.Load(writeConfig(t, configured))
	if err == nil || !strings.Contains(err.Error(), "license_key") {
		t.Fatalf("error = %v", err)
	}
}

func TestCorruptManagedStateNeverFallsBackToYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	if err := os.WriteFile(path, []byte(`{"mode":"broken"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bootstrap := config.Relay{Enabled: true, URL: "https://bootstrap.example.com"}
	_, err := config.NewRelayResolver(config.NewRelayStateStore(path), &fakeLicenseStore{}).Resolve(context.Background(), bootstrap)
	if err == nil || !strings.Contains(err.Error(), "managed relay state") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolverGeneratesAndPersistsOneSessionConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	bootstrap := config.Relay{Enabled: true, URL: "https://relay.example.com"}
	const workers = 40
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Construct independently to exercise the per-path lock rather than
			// relying on all callers to retain one store pointer.
			resolver := config.NewRelayResolver(config.NewRelayStateStore(path), &fakeLicenseStore{})
			resolved, err := resolver.Resolve(context.Background(), bootstrap)
			if err != nil {
				errs <- err
				return
			}
			ids <- resolved.SessionID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("session ids differ: %q and %q", first, id)
		}
	}
	loaded, exists, err := store.Load()
	if err != nil || !exists || loaded.SessionID != first {
		t.Fatalf("persisted = %#v exists=%v err=%v", loaded, exists, err)
	}
}

func TestLicenseStoreReplacementAndClearAreExplicit(t *testing.T) {
	var store config.LicenseStore = &fakeLicenseStore{}
	ctx := context.Background()
	if _, err := store.Load(ctx); !errors.Is(err, config.ErrLicenseNotFound) {
		t.Fatalf("initial load error = %v", err)
	}
	if err := store.Replace(ctx, "rl_test_replacement"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx); err != nil || got != "rl_test_replacement" {
		t.Fatalf("load = %q, %v", got, err)
	}
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, config.ErrLicenseNotFound) {
		t.Fatalf("load after clear error = %v", err)
	}
}
