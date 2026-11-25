// Package pairing composes the code a phone scans to pair with this desktop.
//
// It exists so there is exactly one builder of that code. The CLI and the
// menu-bar app each had their own, and when the format grew relay fields the
// CLI's learned about them and the app's did not -- so every phone paired
// from the desktop app had no relay and no way to know why. The service
// already holds the config, the relay identity and the token; it is the
// natural place to answer "what should this phone scan", and every surface
// that shows a QR renders what it is handed.
package pairing

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/relay"
	core "github.com/croutoncreations/redline/mobile/core"
)

// RelayOnlyHost is the sentinel a relay-only code carries in place of a
// tailnet host. Defined by the phone's parser, since that is what reads it;
// re-exported here so callers on this side have one name for it.
const RelayOnlyHost = core.RelayOnlyHost

// Route names one way a phone can reach the desktop.
type Route string

const (
	RouteDirect Route = "direct"
	RouteRelay  Route = "relay"
)

// RelayRefusalReason is a stable explanation for omitting the relay route.
// Direct pairing can still proceed with this attached to the composed result.
type RelayRefusalReason string

const (
	RelayRefusalOff          RelayRefusalReason = "off"
	RelayRefusalConnecting   RelayRefusalReason = "connecting"
	RelayRefusalNeedsLicense RelayRefusalReason = "needs_license"
	RelayRefusalUnavailable  RelayRefusalReason = "unavailable"
	RelayRefusalLapsed       RelayRefusalReason = "lapsed"
	RelayRefusalNoSeat       RelayRefusalReason = "no_seat"
	RelayRefusalInvalidKey   RelayRefusalReason = "invalid_key"
)

// RelayUnavailableError gives relay-only callers a machine-readable refusal.
type RelayUnavailableError struct{ Reason RelayRefusalReason }

func (e *RelayUnavailableError) Error() string {
	return "relay-only pairing is unavailable: " + string(e.Reason)
}

// ErrNoRoute means the desktop has neither a trusted host nor a relay, so
// there is nowhere to send a phone. The pairing token is still valid for the
// web /pair page on this machine, which is why callers get a typed error they
// can report rather than a refusal to mint.
var ErrNoRoute = errors.New("no trusted host is configured and the relay is not enabled")

// Options selects the direct endpoint, when there is a choice.
type Options struct {
	// Host overrides detection. It must be one of api.trusted_hosts; a host
	// the service will not accept connections for is a QR that fails later
	// with nothing on screen to say why.
	Host string
	// Port applies when Host does not carry one. Zero means 443.
	Port int
	// RelayOnly omits the direct endpoint even when one is available, for a
	// user on the tailnet now who wants the code they will use away from it.
	RelayOnly bool
	// DetectHost supplies a tailnet name when Host is empty. May be nil, in
	// which case the first trusted host is used. Only a detected name that is
	// also trusted is accepted.
	DetectHost func() (string, error)
}

// Code is what a phone scans, plus what it offers, for the surface showing it
// to explain.
type Code struct {
	URL          string
	Routes       []Route
	RelayRefusal RelayRefusalReason
	// Endpoint is the direct host:port the code names, or empty for a
	// relay-only code.
	Endpoint string
	// Notice is something the caller should tell the user that did not stop a
	// code from being produced. Today: tailnet detection failed and a trusted
	// host or the relay was used instead, so the code is fine but Tailscale
	// on this machine may not be.
	Notice string
}

// CallerError marks an invalid caller-controlled pairing option. API handlers
// map it to 400; identity and other internal failures remain 500 errors.
type CallerError struct{ Err error }

func (e *CallerError) Error() string { return e.Err.Error() }
func (e *CallerError) Unwrap() error { return e.Err }

// Plan is the pure result of selecting pairing routes. It contains no desktop
// key and planning performs no filesystem or identity I/O.
type Plan struct {
	Routes       []Route
	Endpoint     string
	Notice       string
	RelayRefusal RelayRefusalReason

	host      string
	port      int
	relayURL  string
	sessionID string
}

// PreparedPlan has completed all fallible identity work. Rendering it with a
// freshly minted token is deterministic and cannot fail.
type PreparedPlan struct {
	plan       Plan
	desktopKey string
}

// PlanRoutes validates caller options and selects routes from one immutable
// runtime snapshot. It is deliberately pure so invalid requests cannot create
// the relay identity file.
func PlanRoutes(trustedHosts []string, runtime config.ResolvedRelay, options Options) (Plan, error) {
	if options.Port < 0 || options.Port > 65535 {
		return Plan{}, &CallerError{Err: errors.New("pairing port must be zero or between 1 and 65535")}
	}
	hasRelay := runtime.CanAdvertise()
	refusal := relayRefusal(runtime)
	if hasRelay && (strings.TrimSpace(runtime.URL) == "" || strings.TrimSpace(runtime.SessionID) == "") {
		return Plan{}, errors.New("dialable relay runtime is missing its URL or session_id")
	}
	host, port, notice, err := chooseDirect(trustedHosts, options, hasRelay)
	if err != nil {
		return Plan{}, err
	}
	if host == "" && !hasRelay {
		if options.RelayOnly {
			return Plan{}, &CallerError{Err: &RelayUnavailableError{Reason: refusal}}
		}
		return Plan{RelayRefusal: refusal}, ErrNoRoute
	}

	plan := Plan{Notice: notice, RelayRefusal: refusal, host: host, port: port}
	if host != "" {
		plan.Routes = append(plan.Routes, RouteDirect)
		plan.Endpoint = host
		if port != 443 {
			plan.Endpoint = net.JoinHostPort(host, strconv.Itoa(port))
		}
	} else {
		plan.host = RelayOnlyHost
		plan.port = 443
	}
	if hasRelay {
		plan.Routes = append(plan.Routes, RouteRelay)
		plan.relayURL = runtime.URL
		plan.sessionID = runtime.SessionID
	}
	return plan, nil
}

func relayRefusal(runtime config.ResolvedRelay) RelayRefusalReason {
	if runtime.CanAdvertise() {
		return ""
	}
	if runtime.CanDial() {
		return RelayRefusalConnecting
	}
	switch runtime.Readiness {
	case config.RelayReadinessNeedsLicense:
		return RelayRefusalNeedsLicense
	case config.RelayReadinessHostedConfigured:
		return RelayRefusalConnecting
	case config.RelayReadinessUnavailable:
		return RelayRefusalUnavailable
	case config.RelayReadinessLapsed:
		return RelayRefusalLapsed
	case config.RelayReadinessNoSeat:
		return RelayRefusalNoSeat
	case config.RelayReadinessInvalidKey:
		return RelayRefusalInvalidKey
	default:
		return RelayRefusalOff
	}
}

// PrepareIdentity performs the only filesystem operation in pairing
// composition, after pure request validation has succeeded.
func PrepareIdentity(plan Plan, keypairPath string) (PreparedPlan, error) {
	prepared := PreparedPlan{plan: plan}
	if plan.relayURL == "" {
		return prepared, nil
	}
	keypair, err := relay.LoadOrCreateKeypair(keypairPath)
	if err != nil {
		return PreparedPlan{}, err
	}
	prepared.desktopKey = core.DesktopPublicKey(keypair)
	return prepared, nil
}

// Render inserts the one-time token after all fallible work has completed.
func (p PreparedPlan) Render(token string) Code {
	return Code{
		URL:          URL(p.plan.host, p.plan.port, token, p.plan.relayURL, p.desktopKey, p.plan.sessionID),
		Routes:       append([]Route(nil), p.plan.Routes...),
		Endpoint:     p.plan.Endpoint,
		Notice:       p.plan.Notice,
		RelayRefusal: p.plan.RelayRefusal,
	}
}

// chooseDirect picks the tailnet endpoint, or none, and says if anything
// worth mentioning happened on the way.
//
// Precedence: an explicit host, then a detected tailnet name that is trusted,
// then the first trusted host. A trusted host may carry its own port
// ("name.ts.net:8443"), which is how a Tailscale Serve front end off 443 is
// written down; that port wins over the option, because the option is a
// default and the config is a fact.
func chooseDirect(trustedHosts []string, options Options, hasRelay bool) (host string, port int, notice string, err error) {
	if options.RelayOnly {
		return "", 0, "", nil
	}
	port = options.Port
	if port == 0 {
		port = 443
	}
	if options.Host != "" {
		wanted := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(options.Host), "."))
		trustedHost, trustedPort, ok := trusted(trustedHosts, wanted)
		if !ok {
			return "", 0, "", &CallerError{Err: errors.New("host " + strconv.Quote(wanted) + " is not listed in api.trusted_hosts")}
		}
		return trustedHost, portOr(trustedPort, port), "", nil
	}
	if options.DetectHost != nil {
		detected, detectErr := options.DetectHost()
		if detectErr == nil {
			if trustedHost, trustedPort, ok := trusted(trustedHosts, detected); ok {
				return trustedHost, portOr(trustedPort, port), "", nil
			}
		} else if len(trustedHosts) == 0 && !hasRelay {
			// Nothing to fall back to, so the detection failure is the answer.
			return "", 0, "", detectErr
		} else {
			// A code can still be produced, so this is not a failure -- but
			// the reason detection failed is usually "Tailscale is not
			// running", which the person about to scan a tailnet code would
			// want to know. Dropping it here is how a real fault hides behind
			// a working fallback.
			notice = "could not detect the Tailscale name (" + detectErr.Error() + "); using the configured host"
		}
	}
	for _, entry := range trustedHosts {
		entryHost, entryPort := splitHostPort(entry)
		if entryHost == "" {
			continue
		}
		return entryHost, portOr(entryPort, port), notice, nil
	}
	return "", 0, notice, nil
}

// portOr returns the port a trusted-host entry named, or the default when it
// named none.
func portOr(fromEntry, fallback int) int {
	if fromEntry != 0 {
		return fromEntry
	}
	return fallback
}

// trusted reports whether host is in api.trusted_hosts, and the port that
// entry names if any. Entries may carry a port and the candidate may not, so
// the comparison is on the host alone.
func trusted(trustedHosts []string, host string) (string, int, bool) {
	candidate, _ := splitHostPort(host)
	for _, entry := range trustedHosts {
		entryHost, entryPort := splitHostPort(entry)
		if strings.EqualFold(entryHost, candidate) {
			return entryHost, entryPort, true
		}
	}
	return "", 0, false
}

func splitHostPort(entry string) (string, int) {
	entry = strings.ToLower(strings.TrimSpace(entry))
	host, portText, err := net.SplitHostPort(entry)
	if err != nil {
		return entry, 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return host, 0
	}
	return host, port
}

// URL builds the URL encoded into the pairing QR from already-chosen parts.
//
// Relay, key and session travel together or not at all: without the key the
// phone cannot verify who answers, and without the session it cannot find
// this desktop on the relay. Host entitlement is deliberately never included:
// phones are admitted only while an entitled host is attached.
//
// Omitting absent fields rather than sending them empty keeps a QR from a
// relay-less desktop byte-identical to the one this has always produced, so
// an older phone and a newer one read it the same way.
func URL(host string, port int, token, relayURL, desktopKey, sessionID string) string {
	endpoint := host
	if port != 443 {
		endpoint = net.JoinHostPort(host, strconv.Itoa(port))
	}
	pairingURL := url.URL{Scheme: "https", Host: endpoint, Path: "/pair"}
	fragment := url.Values{}
	fragment.Set("pairing_token", token)
	if relayURL != "" && desktopKey != "" && sessionID != "" {
		fragment.Set("relay", relayURL)
		fragment.Set("key", desktopKey)
		fragment.Set("session", sessionID)
	}
	// Fragment holds the decoded form and RawFragment the encoded one; they
	// have to agree or url.URL falls back to re-escaping Fragment, and an
	// already-encoded string escaped twice turns %2B into %252B. Tokens are
	// base64url and never contain those bytes; keys are standard base64 and
	// always might.
	encoded := fragment.Encode()
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		decoded = encoded
	}
	pairingURL.Fragment = decoded
	pairingURL.RawFragment = encoded
	return pairingURL.String()
}
