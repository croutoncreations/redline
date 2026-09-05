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

	"github.com/jfox/redline/internal/config"
	"github.com/jfox/redline/internal/relay"
	core "github.com/jfox/redline/mobile/core"
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
	URL    string
	Routes []Route
	// Endpoint is the direct host:port the code names, or empty for a
	// relay-only code.
	Endpoint string
	// Notice is something the caller should tell the user that did not stop a
	// code from being produced. Today: tailnet detection failed and a trusted
	// host or the relay was used instead, so the code is fine but Tailscale
	// on this machine may not be.
	Notice string
}

// Compose builds the pairing code for token against this configuration.
//
// The direct endpoint is chosen from Options.Host, else a detected name that
// is trusted, else the first trusted host. The relay fields are added whenever
// relay.enabled, from the same identity the desktop's relay leg presents:
// loading the keypair here rather than generating one is what makes the key
// in the QR the key the phone will later be answered by.
func Compose(cfg config.Config, token string, options Options) (Code, error) {
	relayURL, desktopKey, sessionID, entitlement := "", "", "", ""
	if cfg.Relay.Enabled {
		keypair, err := relay.LoadOrCreateKeypair(relay.DefaultKeypairPath(cfg.Relay.KeypairPath, cfg.Database))
		if err != nil {
			return Code{}, err
		}
		// Without the session id the phone knows where the relay is and has
		// no idea which session on it belongs to this desktop.
		sessionID = strings.TrimSpace(cfg.Relay.SessionID)
		if sessionID == "" {
			return Code{}, errors.New("relay.enabled is set but relay.session_id is empty; add one to the config")
		}
		relayURL = cfg.Relay.URL
		desktopKey = core.DesktopPublicKey(keypair)
		// Presented by the phone to the relay as its own authorisation. It
		// says nothing about who the user is, so handing it to a paired device
		// grants relay access and nothing else. Empty for a self-hosted relay
		// run with ALLOW_UNENTITLED=true.
		entitlement = strings.TrimSpace(cfg.Relay.EntitlementToken)
	}
	hasRelay := relayURL != "" && desktopKey != "" && sessionID != ""

	host, port, notice, err := chooseDirect(cfg, options, hasRelay)
	if err != nil {
		return Code{}, err
	}
	if host == "" && !hasRelay {
		return Code{}, ErrNoRoute
	}

	code := Code{Notice: notice}
	qrHost := host
	if host != "" {
		code.Routes = append(code.Routes, RouteDirect)
		code.Endpoint = host
		if port != 443 {
			code.Endpoint = net.JoinHostPort(host, strconv.Itoa(port))
		}
	} else {
		qrHost = RelayOnlyHost
		port = 443
	}
	if hasRelay {
		code.Routes = append(code.Routes, RouteRelay)
	}
	code.URL = URL(qrHost, port, token, relayURL, desktopKey, sessionID, entitlement)
	return code, nil
}

// chooseDirect picks the tailnet endpoint, or none, and says if anything
// worth mentioning happened on the way.
//
// Precedence: an explicit host, then a detected tailnet name that is trusted,
// then the first trusted host. A trusted host may carry its own port
// ("name.ts.net:8443"), which is how a Tailscale Serve front end off 443 is
// written down; that port wins over the option, because the option is a
// default and the config is a fact.
func chooseDirect(cfg config.Config, options Options, hasRelay bool) (host string, port int, notice string, err error) {
	if options.RelayOnly {
		return "", 0, "", nil
	}
	port = options.Port
	if port == 0 {
		port = 443
	}
	if options.Host != "" {
		wanted := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(options.Host), "."))
		trustedHost, trustedPort, ok := trusted(cfg, wanted)
		if !ok {
			return "", 0, "", errors.New("host " + strconv.Quote(wanted) + " is not listed in api.trusted_hosts")
		}
		return trustedHost, portOr(trustedPort, port), "", nil
	}
	if options.DetectHost != nil {
		detected, detectErr := options.DetectHost()
		if detectErr == nil {
			if trustedHost, trustedPort, ok := trusted(cfg, detected); ok {
				return trustedHost, portOr(trustedPort, port), "", nil
			}
		} else if len(cfg.API.TrustedHosts) == 0 && !hasRelay {
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
	for _, entry := range cfg.API.TrustedHosts {
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
func trusted(cfg config.Config, host string) (string, int, bool) {
	candidate, _ := splitHostPort(host)
	for _, entry := range cfg.API.TrustedHosts {
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
// this desktop on the relay. The entitlement is published when present but
// not required for the other three -- a self-hosted relay run with
// ALLOW_UNENTITLED=true has none, and an open relay does not ask.
//
// Omitting absent fields rather than sending them empty keeps a QR from a
// relay-less desktop byte-identical to the one this has always produced, so
// an older phone and a newer one read it the same way.
func URL(host string, port int, token, relayURL, desktopKey, sessionID, entitlement string) string {
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
		if entitlement != "" {
			fragment.Set("entitlement", entitlement)
		}
	}
	// Fragment holds the decoded form and RawFragment the encoded one; they
	// have to agree or url.URL falls back to re-escaping Fragment, and an
	// already-encoded string escaped twice turns %2B into %252B. Tokens are
	// base64url and never contain those bytes; keys and entitlements are
	// standard base64 and always might.
	encoded := fragment.Encode()
	decoded, err := url.PathUnescape(encoded)
	if err != nil {
		decoded = encoded
	}
	pairingURL.Fragment = decoded
	pairingURL.RawFragment = encoded
	return pairingURL.String()
}
