package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jfox/redline/internal/config"
)

type fakeLicenseStore struct {
	mu      sync.Mutex
	value   string
	loadErr error
	loads   int
}

func (s *fakeLicenseStore) Load(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return "", s.loadErr
	}
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

func TestRelayBootstrapValidationAgreesWithServiceResolution(t *testing.T) {
	tests := []struct {
		name      string
		relayYAML string
		wantError string
		wantMode  config.RelayMode
		wantURL   string
	}{
		{name: "custom URL", relayYAML: "  enabled: true\n  url: https://relay.example.com", wantMode: config.RelayModeSelfHosted, wantURL: "https://relay.example.com"},
		{name: "custom issuer", relayYAML: "  enabled: true\n  issuer_url: https://issuer.example.com/api", wantMode: config.RelayModeHosted, wantURL: config.DefaultHostedRelayURL},
		{name: "custom URL and issuer contradict", relayYAML: "  enabled: true\n  url: https://relay.example.com\n  issuer_url: https://issuer.example.com/api", wantError: "issuer_url"},
		{name: "unsafe custom URL", relayYAML: "  enabled: true\n  url: http://relay.example.com", wantError: "relay url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured := strings.Replace(validConfig, "active_policy: standard", "active_policy: standard\nrelay:\n"+tt.relayYAML, 1)
			path := writeConfig(t, configured)
			_, publicErr := config.Load(path)
			serviceConfig, serviceLoadErr := config.LoadForService(path)
			if serviceLoadErr != nil {
				t.Fatalf("service structural load: %v", serviceLoadErr)
			}
			resolved, resolveErr := config.NewRelayResolver(
				config.NewRelayStateStore(filepath.Join(t.TempDir(), "relay-state.json")),
				&fakeLicenseStore{},
			).Resolve(context.Background(), serviceConfig.Relay)
			if tt.wantError != "" {
				if publicErr == nil || resolveErr == nil || !strings.Contains(publicErr.Error(), tt.wantError) || !strings.Contains(resolveErr.Error(), tt.wantError) {
					t.Fatalf("public error=%v service error=%v; both must contain %q", publicErr, resolveErr, tt.wantError)
				}
				return
			}
			if publicErr != nil || resolveErr != nil {
				t.Fatalf("public error=%v service error=%v", publicErr, resolveErr)
			}
			if resolved.Mode != tt.wantMode || resolved.URL != tt.wantURL {
				t.Fatalf("resolved=%#v", resolved)
			}
		})
	}
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
		{name: "yaml self hosted never reads license", bootstrap: config.Relay{Enabled: true, URL: "https://relay.example.com"}, license: "rl_fake_boundary_sentinel_never_read", wantMode: config.RelayModeSelfHosted, wantURL: "https://relay.example.com", wantStatus: config.RelayReadinessSelfHosted, wantDial: true},
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

func TestYAMLRejectsManagedSessionAndHostSecrets(t *testing.T) {
	for _, field := range []string{"license_key", "entitlement_token", "session_id"} {
		t.Run(field, func(t *testing.T) {
			configured := strings.Replace(validConfig, "active_policy: standard", "active_policy: standard\nrelay:\n  enabled: true\n  "+field+": must_not_parse", 1)
			path := writeConfig(t, configured)
			for name, load := range map[string]func(string) (config.Config, error){"standard": config.Load, "service": config.LoadForService} {
				_, err := load(path)
				if err == nil || !strings.Contains(err.Error(), field) {
					t.Fatalf("%s load error = %v", name, err)
				}
			}
		})
	}
}

func TestEffectiveRelayValidationUsesManagedPrecedence(t *testing.T) {
	configured := strings.Replace(validConfig, "active_policy: standard", "active_policy: standard\napi:\n  trusted_hosts: [public.example.com]\nrelay:\n  enabled: true\n  url: http://stale.invalid", 1)
	cfg, err := config.LoadForService(writeConfig(t, configured))
	if err != nil {
		t.Fatalf("structural load rejected losing bootstrap: %v", err)
	}

	t.Run("managed off does not relax hosts", func(t *testing.T) {
		resolved := config.ResolvedRelay{RelayManagedState: config.RelayManagedState{Mode: config.RelayModeOff}}
		if err := config.ValidateEffectiveRelay(cfg, resolved); err == nil || !strings.Contains(err.Error(), ".ts.net") {
			t.Fatalf("effective validation error = %v", err)
		}
	})

	t.Run("valid managed state ignores stale invalid YAML", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "relay-state.json")
		store := config.NewRelayStateStore(path)
		managed := config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "https://managed.example.com", SessionID: "managed-session-abcdefghij"}
		if err := store.Save(managed); err != nil {
			t.Fatal(err)
		}
		resolved, err := config.NewRelayResolver(store, &fakeLicenseStore{}).Resolve(context.Background(), cfg.Relay)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.URL != managed.URL {
			t.Fatalf("resolved URL = %q", resolved.URL)
		}
		if err := config.ValidateEffectiveRelay(cfg, resolved); err != nil {
			t.Fatalf("managed remote mode should permit valid public host: %v", err)
		}
	})
}

func TestLicenseSentinelNeverCrossesResolutionBoundary(t *testing.T) {
	const sentinel = "rl_fake_boundary_sentinel_never_emit"
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	resolved, err := config.NewRelayResolver(store, &fakeLicenseStore{value: sentinel}).Resolve(context.Background(), config.Relay{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	stateBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"state": string(stateBytes), "resolved JSON": string(jsonBytes), "diagnostic/log formatting": fmt.Sprint(resolved), "argv": strings.Join(os.Args, "\x00"), "environment": strings.Join(os.Environ(), "\x00")} {
		if strings.Contains(contents, sentinel) {
			t.Fatalf("license sentinel crossed %s boundary", name)
		}
	}
}

func TestUnavailableLicenseStoreDisablesOnlyHostedRelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	if err := store.Save(config.RelayManagedState{Mode: config.RelayModeHosted, URL: config.DefaultHostedRelayURL, IssuerURL: config.DefaultIssuerURL, SessionID: "managed-session-abcdefghij"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.NewRelayResolver(store, &fakeLicenseStore{loadErr: config.ErrLicenseStoreUnavailable}).Resolve(context.Background(), config.Relay{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Readiness != config.RelayReadinessUnavailable || resolved.Dial {
		t.Fatalf("resolved = %#v", resolved)
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

func TestRelayStateSubprocessHelper(t *testing.T) {
	if os.Getenv("REDLINE_RELAY_STATE_HELPER") != "1" {
		return
	}
	path, action, value := os.Getenv("REDLINE_RELAY_STATE_PATH"), os.Getenv("REDLINE_RELAY_STATE_ACTION"), os.Getenv("REDLINE_RELAY_STATE_VALUE")
	store := config.NewRelayStateStore(path)
	switch action {
	case "resolve":
		resolved, err := config.NewRelayResolver(store, &fakeLicenseStore{}).Resolve(context.Background(), config.Relay{Enabled: true, URL: "https://relay.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Print(resolved.SessionID)
	case "append":
		_, err := store.Update(func(state config.RelayManagedState, exists bool) (config.RelayManagedState, error) {
			if !exists {
				return state, errors.New("missing state")
			}
			state.Label += value
			return state, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper action %q", action)
	}
	os.Exit(0)
}

func runRelayStateHelpers(t *testing.T, path, action string, values []string) []string {
	t.Helper()
	outputs := make([]string, len(values))
	errs := make(chan error, len(values))
	var wg sync.WaitGroup
	for index, value := range values {
		wg.Add(1)
		go func(index int, value string) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRelayStateSubprocessHelper$")
			cmd.Env = append(os.Environ(), "REDLINE_RELAY_STATE_HELPER=1", "REDLINE_RELAY_STATE_PATH="+path, "REDLINE_RELAY_STATE_ACTION="+action, "REDLINE_RELAY_STATE_VALUE="+value)
			output, err := cmd.CombinedOutput()
			if err != nil {
				errs <- fmt.Errorf("helper %d: %w: %s", index, err, output)
				return
			}
			outputs[index] = string(output)
		}(index, value)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	return outputs
}

func TestResolverGeneratesOneSessionAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	outputs := runRelayStateHelpers(t, path, "resolve", []string{"a", "b", "c", "d", "e", "f"})
	for _, output := range outputs[1:] {
		if output != outputs[0] {
			t.Fatalf("session IDs differ across processes: %q and %q", outputs[0], output)
		}
	}
	state, exists, err := config.NewRelayStateStore(path).Load()
	if err != nil || !exists || state.SessionID != outputs[0] {
		t.Fatalf("persisted state=%#v exists=%v err=%v outputs=%q", state, exists, err, outputs)
	}
}

func TestRelayStateUpdateDoesNotLoseCrossProcessMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	if err := store.Save(config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "https://relay.example.com", SessionID: "session-abcdefghij0123"}); err != nil {
		t.Fatal(err)
	}
	values := []string{"A", "B", "C", "D", "E", "F"}
	runRelayStateHelpers(t, path, "append", values)
	state, _, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if strings.Count(state.Label, value) != 1 {
			t.Fatalf("lost update %q in label %q", value, state.Label)
		}
	}
}

func TestRelayStatePrePublicationFailurePreservesOldState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-state.json")
	store := config.NewRelayStateStore(path)
	original := config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "https://relay.example.com", SessionID: "session-abcdefghij0123"}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config.RelayManagedState{Mode: config.RelayModeSelfHosted, URL: "http://unsafe.example.com", SessionID: original.SessionID}); err == nil {
		t.Fatal("invalid interrupted replacement unexpectedly succeeded")
	}
	loaded, exists, err := store.Load()
	if err != nil || !exists || loaded != original {
		t.Fatalf("old state not preserved: loaded=%#v exists=%v err=%v", loaded, exists, err)
	}
}

func TestRelayStateRejectsSymlinkAndHostileParent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "relay-state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.NewRelayStateStore(link).Load(); err == nil {
		t.Fatal("accepted symlink relay state")
	}
	hostile := filepath.Join(root, "hostile")
	if err := os.Mkdir(hostile, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(hostile, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := config.NewRelayStateStore(filepath.Join(hostile, "state.json")).Save(config.RelayManagedState{Mode: config.RelayModeOff}); err == nil || !strings.Contains(err.Error(), "directory permissions") {
		t.Fatalf("hostile parent error = %v", err)
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
