package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/jfox/redline/internal/relay"
	core "github.com/jfox/redline/mobile/core"
)

const (
	DefaultHostedRelayURL = "https://redline-relay.croutoncreations.com"
	DefaultIssuerURL      = "https://redline.croutoncreations.com/api"
)

type RelayMode string

const (
	RelayModeOff        RelayMode = "off"
	RelayModeHosted     RelayMode = "hosted"
	RelayModeSelfHosted RelayMode = "self_hosted"
)

// RelayManagedState is the complete non-secret state owned by the service and,
// in Phase 3, the Pair a Device flow. Its JSON shape is intentionally closed:
// licenses and entitlement tokens belong in dedicated secret stores.
type RelayManagedState struct {
	Mode      RelayMode `json:"mode"`
	URL       string    `json:"url"`
	IssuerURL string    `json:"issuer_url"`
	Label     string    `json:"label"`
	SessionID string    `json:"session_id"`
}

// RelayStateStore serializes access to the managed state and replaces the file
// atomically. Stores for the same cleaned path share a process-local lock, so
// independently constructed service components cannot lose updates.
type RelayStateStore struct {
	path string
	mu   *sync.Mutex
}

var relayStateLocks sync.Map

func NewRelayStateStore(path string) *RelayStateStore {
	cleaned := filepath.Clean(path)
	lock, _ := relayStateLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &RelayStateStore{path: cleaned, mu: lock.(*sync.Mutex)}
}

func (s *RelayStateStore) Path() string { return s.path }

// DefaultRelayStatePath keeps managed state beside the Noise identity. This is
// also beside the database when the identity path is not overridden.
func DefaultRelayStatePath(keypairPath, databasePath string) string {
	return filepath.Join(filepath.Dir(relay.DefaultKeypairPath(keypairPath, databasePath)), "relay-state.json")
}

func (s *RelayStateStore) Load() (RelayManagedState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *RelayStateStore) loadLocked() (RelayManagedState, bool, error) {
	info, err := os.Lstat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RelayManagedState{}, false, nil
		}
		return RelayManagedState{}, false, fmt.Errorf("inspect relay state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return RelayManagedState{}, false, fmt.Errorf("relay state must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return RelayManagedState{}, false, fmt.Errorf("relay state permissions %#o are invalid; want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return RelayManagedState{}, false, fmt.Errorf("read relay state: %w", err)
	}
	var state RelayManagedState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return RelayManagedState{}, false, fmt.Errorf("decode relay state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return RelayManagedState{}, false, fmt.Errorf("decode relay state: multiple JSON values")
		}
		return RelayManagedState{}, false, fmt.Errorf("decode relay state: %w", err)
	}
	if err := validateRelayManagedState(state); err != nil {
		return RelayManagedState{}, false, err
	}
	return state, true, nil
}

func (s *RelayStateStore) Save(state RelayManagedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(state)
}

// Update applies fn while holding the store lock, so read-modify-write callers
// cannot create two session IDs or lose a concurrent managed choice.
func (s *RelayStateStore) Update(fn func(RelayManagedState, bool) (RelayManagedState, error)) (RelayManagedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists, err := s.loadLocked()
	if err != nil {
		return RelayManagedState{}, err
	}
	next, err := fn(current, exists)
	if err != nil {
		return RelayManagedState{}, err
	}
	if err := s.saveLocked(next); err != nil {
		return RelayManagedState{}, err
	}
	return next, nil
}

func (s *RelayStateStore) saveLocked(state RelayManagedState) error {
	if err := validateRelayManagedState(state); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode relay state: %w", err)
	}
	raw = append(raw, '\n')
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create relay state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".relay-state-*")
	if err != nil {
		return fmt.Errorf("create temporary relay state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	fail := func(operation string, cause error) error {
		_ = temporary.Close()
		return fmt.Errorf("%s relay state: %w", operation, cause)
	}
	if err := temporary.Chmod(0o600); err != nil {
		return fail("protect temporary", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		return fail("write temporary", err)
	}
	if err := temporary.Sync(); err != nil {
		return fail("sync temporary", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary relay state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace relay state: %w", err)
	}
	// Rename preserves the temporary's 0600 mode. Chmod is defense in depth for
	// platforms with unusual rename semantics and repairs no existing file in
	// place (the replacement has already occurred).
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("protect relay state: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		if syncErr := directoryHandle.Sync(); syncErr != nil {
			_ = directoryHandle.Close()
			return fmt.Errorf("sync relay state directory: %w", syncErr)
		}
		_ = directoryHandle.Close()
	}
	return nil
}

var relaySessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func validateRelayManagedState(state RelayManagedState) error {
	if len(state.Label) > 200 || strings.IndexFunc(state.Label, unicode.IsControl) >= 0 {
		return fmt.Errorf("relay label must be at most 200 characters and contain no control characters")
	}
	if state.SessionID != "" && !relaySessionPattern.MatchString(state.SessionID) {
		return fmt.Errorf("relay session_id must contain 16 to 128 base64url characters")
	}
	switch state.Mode {
	case RelayModeOff:
		if state.URL != "" || state.IssuerURL != "" || state.SessionID != "" {
			return fmt.Errorf("off relay state must not retain connection settings")
		}
	case RelayModeHosted:
		if state.URL != "" && state.URL != DefaultHostedRelayURL {
			return fmt.Errorf("hosted relay mode must use %s", DefaultHostedRelayURL)
		}
		if state.IssuerURL != "" {
			if err := validateSafeEndpoint(state.IssuerURL); err != nil {
				return fmt.Errorf("relay issuer_url: %w", err)
			}
		}
	case RelayModeSelfHosted:
		if state.URL == "" {
			return fmt.Errorf("self_hosted relay mode requires a custom url")
		}
		if state.URL == DefaultHostedRelayURL {
			return fmt.Errorf("self_hosted relay mode requires a custom url")
		}
		if err := validateSafeEndpoint(state.URL); err != nil {
			return fmt.Errorf("relay url: %w", err)
		}
		if state.IssuerURL != "" {
			return fmt.Errorf("self_hosted relay mode must not configure an issuer")
		}
	default:
		return fmt.Errorf("relay mode %q must be off, hosted, or self_hosted", state.Mode)
	}
	return nil
}

func validateSafeEndpoint(raw string) error {
	if err := core.ValidateRelayURL(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%q must not contain a query or fragment", raw)
	}
	return nil
}

// RelayReadiness is the configuration-only state available in Phase 2.1.
// Token acquisition and active/renewing states are introduced in Phase 2.2.
type RelayReadiness string

const (
	RelayReadinessOff              RelayReadiness = "off"
	RelayReadinessSelfHosted       RelayReadiness = "self_hosted"
	RelayReadinessNeedsLicense     RelayReadiness = "needs_license"
	RelayReadinessHostedConfigured RelayReadiness = "hosted_configured"
)

type ResolvedRelay struct {
	RelayManagedState
	Readiness RelayReadiness
	// Dial is true only when Phase 2.1 has everything required to connect.
	// Hosted mode remains false until Phase 2.2 can exchange its license for an
	// entitlement token; the license itself is never passed to the relay.
	Dial bool
}

// RelayResolver is the single precedence boundary between managed state and
// bootstrap YAML. Once a managed file exists, YAML relay fields are ignored.
type RelayResolver struct {
	state    *RelayStateStore
	licenses LicenseStore
}

func NewRelayResolver(state *RelayStateStore, licenses LicenseStore) *RelayResolver {
	return &RelayResolver{state: state, licenses: licenses}
}

func (r *RelayResolver) Resolve(ctx context.Context, bootstrap Relay) (ResolvedRelay, error) {
	state, managed, err := r.state.Load()
	if err != nil {
		return ResolvedRelay{}, fmt.Errorf("managed relay state: %w", err)
	}
	if !managed && !bootstrap.Enabled {
		return ResolvedRelay{RelayManagedState: RelayManagedState{Mode: RelayModeOff}, Readiness: RelayReadinessOff}, nil
	}
	if managed && state.Mode == RelayModeOff {
		return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessOff}, nil
	}

	state, err = r.state.Update(func(current RelayManagedState, exists bool) (RelayManagedState, error) {
		if exists {
			state = current
		} else if strings.TrimSpace(bootstrap.URL) == "" {
			state = RelayManagedState{Mode: RelayModeHosted, IssuerURL: strings.TrimSpace(bootstrap.IssuerURL)}
		} else {
			state = RelayManagedState{Mode: RelayModeSelfHosted, URL: strings.TrimSpace(bootstrap.URL)}
		}
		if state.Mode == RelayModeHosted {
			if state.URL == "" {
				state.URL = DefaultHostedRelayURL
			}
			if state.IssuerURL == "" {
				state.IssuerURL = DefaultIssuerURL
			}
		}
		if state.SessionID == "" {
			generated, err := relay.NewSessionID()
			if err != nil {
				return RelayManagedState{}, err
			}
			state.SessionID = generated
		}
		return state, nil
	})
	if err != nil {
		return ResolvedRelay{}, fmt.Errorf("resolve managed relay state: %w", err)
	}

	switch state.Mode {
	case RelayModeSelfHosted:
		return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessSelfHosted, Dial: true}, nil
	case RelayModeHosted:
		if r.licenses == nil {
			return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessNeedsLicense}, nil
		}
		license, err := r.licenses.Load(ctx)
		if errors.Is(err, ErrLicenseNotFound) {
			return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessNeedsLicense}, nil
		}
		if err != nil {
			return ResolvedRelay{}, fmt.Errorf("read hosted relay license: %w", err)
		}
		if strings.TrimSpace(license) == "" {
			return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessNeedsLicense}, nil
		}
		return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessHostedConfigured}, nil
	default:
		return ResolvedRelay{}, fmt.Errorf("managed relay state has unexpected mode %q", state.Mode)
	}
}
