package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
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

// RelayStateStore serializes access with an advisory file lock held across the
// complete read/mutate/fsync/rename/fsync transaction. The normalized absolute
// path gives aliases such as "a/../state" one lock identity across processes.
type RelayStateStore struct {
	path string
	mu   *sync.Mutex
}

var relayStateProcessLocks sync.Map

func NewRelayStateStore(path string) *RelayStateStore {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	cleaned := filepath.Clean(absolute)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(cleaned)); err == nil {
		cleaned = filepath.Join(parent, filepath.Base(cleaned))
	}
	lock, _ := relayStateProcessLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &RelayStateStore{path: cleaned, mu: lock.(*sync.Mutex)}
}

func (s *RelayStateStore) Path() string { return s.path }

// DefaultRelayStatePath keeps managed state beside the Noise identity. This is
// also beside the database when the identity path is not overridden.
func DefaultRelayStatePath(keypairPath, databasePath string) string {
	return filepath.Join(filepath.Dir(relay.DefaultKeypairPath(keypairPath, databasePath)), "relay-state.json")
}

func (s *RelayStateStore) Load() (state RelayManagedState, exists bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := beginRelayStateOperation(s.path)
	if err != nil {
		return RelayManagedState{}, false, err
	}
	defer func() { err = errors.Join(err, op.close()) }()
	return loadRelayState(op)
}

func loadRelayState(op *relayStateOperation) (RelayManagedState, bool, error) {
	raw, exists, err := op.read()
	if err != nil || !exists {
		return RelayManagedState{}, exists, err
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

func (s *RelayStateStore) Save(state RelayManagedState) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := beginRelayStateOperation(s.path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, op.close()) }()
	return saveRelayState(op, state)
}

// Update holds the inter-process lock for the entire transaction, so separate
// service processes cannot create two session IDs or lose concurrent updates.
func (s *RelayStateStore) Update(fn func(RelayManagedState, bool) (RelayManagedState, error)) (next RelayManagedState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := beginRelayStateOperation(s.path)
	if err != nil {
		return RelayManagedState{}, err
	}
	defer func() { err = errors.Join(err, op.close()) }()
	current, exists, err := loadRelayState(op)
	if err != nil {
		return RelayManagedState{}, err
	}
	next, err = fn(current, exists)
	if err != nil {
		return RelayManagedState{}, err
	}
	if err := saveRelayState(op, next); err != nil {
		return RelayManagedState{}, err
	}
	return next, nil
}

func saveRelayState(op *relayStateOperation, state RelayManagedState) error {
	if err := validateRelayManagedState(state); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode relay state: %w", err)
	}
	raw = append(raw, '\n')
	return op.write(raw)
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

// RelayReadiness is the service's published entitlement state. It is kept on
// the same immutable snapshot as the dial inputs so API and supervisor readers
// cannot observe a new token with an old state (or vice versa).
type RelayReadiness string

const (
	RelayReadinessOff              RelayReadiness = "off"
	RelayReadinessSelfHosted       RelayReadiness = "self_hosted"
	RelayReadinessNeedsLicense     RelayReadiness = "needs_license"
	RelayReadinessHostedConfigured RelayReadiness = "hosted_configured"
	RelayReadinessActive           RelayReadiness = "active"
	RelayReadinessRenewPending     RelayReadiness = "renew_pending"
	RelayReadinessUnavailable      RelayReadiness = "unavailable"
	RelayReadinessLapsed           RelayReadiness = "lapsed"
	RelayReadinessNoSeat           RelayReadiness = "no_seat"
	RelayReadinessInvalidKey       RelayReadiness = "invalid_key"
)

// RelayActivationSummary is the non-secret subset returned with no_seat.
type RelayActivationSummary struct {
	Label     string    `json:"label"`
	FirstSeen time.Time `json:"first_seen"`
}

const maxRelayActivationSummaries = 25

// RelayEntitlementToken keeps the runtime-only host credential out of JSON and
// diagnostic formatting while still allowing the dialer boundary to read it.
type RelayEntitlementToken struct {
	value string
}

func NewRelayEntitlementToken(value string) RelayEntitlementToken {
	return RelayEntitlementToken{value: value}
}

func (t RelayEntitlementToken) Value() string { return t.value }

func (t RelayEntitlementToken) String() string {
	if t.value == "" {
		return ""
	}
	return "[REDACTED]"
}

func (t RelayEntitlementToken) GoString() string { return t.String() }

type ResolvedRelay struct {
	RelayManagedState
	Readiness RelayReadiness `json:"state"`
	Dial      bool           `json:"-"`

	EntitlementToken    RelayEntitlementToken `json:"-"`
	RenewsAt            time.Time             `json:"renews_at,omitempty"`
	ExpiresAt           time.Time             `json:"expires_at,omitempty"`
	UnavailableSince    time.Time             `json:"since,omitempty"`
	Seats               int                   `json:"seats,omitempty"`
	SeatsUsed           int                   `json:"seats_used,omitempty"`
	MaxClients          int                   `json:"max_clients,omitempty"`
	ReconnectGeneration uint64                `json:"-"`

	// A fixed-size value keeps snapshots comparable (the supervisor uses value
	// equality) while the count preserves a normal list at API boundaries.
	ActivationSummaries [maxRelayActivationSummaries]RelayActivationSummary `json:"-"`
	ActivationCount     int                                                 `json:"-"`
}

func (r ResolvedRelay) Activations() []RelayActivationSummary {
	count := r.ActivationCount
	if count < 0 {
		count = 0
	}
	if count > len(r.ActivationSummaries) {
		count = len(r.ActivationSummaries)
	}
	out := make([]RelayActivationSummary, count)
	copy(out, r.ActivationSummaries[:count])
	return out
}

// CanDial is the one authority for whether this snapshot may advertise and
// operate a relay route. Dial records resource availability; mode/readiness
// prevent malformed or stale externally supplied snapshots from contradicting
// the state the user sees.
func (r ResolvedRelay) CanDial() bool {
	if !r.Dial || r.Mode == RelayModeOff {
		return false
	}
	switch r.Readiness {
	case RelayReadinessSelfHosted:
		return true
	case RelayReadinessActive, RelayReadinessRenewPending:
		return r.EntitlementToken.Value() != ""
	default:
		return false
	}
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

// ResolveRelayBootstrap is the canonical validation and conversion for active
// YAML bootstrap input. Both the public loader and RelayResolver call this
// function; managed state may ignore invalid losing bootstrap values.
func ResolveRelayBootstrap(bootstrap RelayBootstrap) (RelayManagedState, error) {
	state := RelayManagedState{Mode: RelayModeOff}
	if bootstrap.Enabled {
		relayURL := strings.TrimSpace(bootstrap.URL)
		issuerURL := strings.TrimSpace(bootstrap.IssuerURL)
		if relayURL != "" {
			if issuerURL != "" {
				return RelayManagedState{}, fmt.Errorf("relay issuer_url is only valid for hosted bootstrap mode")
			}
			state = RelayManagedState{Mode: RelayModeSelfHosted, URL: relayURL}
		} else {
			if issuerURL == "" {
				issuerURL = DefaultIssuerURL
			}
			state = RelayManagedState{Mode: RelayModeHosted, URL: DefaultHostedRelayURL, IssuerURL: issuerURL}
		}
	}
	if err := validateRelayManagedState(state); err != nil {
		return RelayManagedState{}, err
	}
	return state, nil
}

func (r *RelayResolver) Resolve(ctx context.Context, bootstrap RelayBootstrap) (ResolvedRelay, error) {
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
		} else {
			state, err = ResolveRelayBootstrap(bootstrap)
			if err != nil {
				return RelayManagedState{}, err
			}
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
		if errors.Is(err, ErrLicenseStoreUnavailable) {
			return ResolvedRelay{RelayManagedState: state, Readiness: RelayReadinessUnavailable}, nil
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
