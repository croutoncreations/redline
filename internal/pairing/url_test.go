package pairing

import (
	"encoding/json"
	"net/url"
	"testing"

	core "github.com/jfox/redline/mobile/core"
)

func TestMobilePairingURLCarriesRelayDetails(t *testing.T) {
	got := URL(
		"macbook.example.ts.net", 443, "one-time-token",
		"https://relay.example.com", "ZGVza3RvcC1rZXk=", "session-abcdefghij0123", "ent-token",
	)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	if fragment.Get("pairing_token") != "one-time-token" {
		t.Fatalf("token: %q", fragment.Get("pairing_token"))
	}
	if fragment.Get("relay") != "https://relay.example.com" {
		t.Fatalf("relay: %q", fragment.Get("relay"))
	}
	if fragment.Get("key") != "ZGVza3RvcC1rZXk=" {
		t.Fatalf("key: %q", fragment.Get("key"))
	}
	// Without this the phone knows where the relay is but not which session
	// on it belongs to this desktop.
	if fragment.Get("session") != "session-abcdefghij0123" {
		t.Fatalf("session: %q", fragment.Get("session"))
	}
}

// With the relay off, the QR must look exactly as it always has, so a phone
// paired against an older desktop and a newer one behave identically.
func TestMobilePairingURLOmitsRelayDetailsWhenUnset(t *testing.T) {
	got := URL("macbook.example.ts.net", 443, "one-time-token", "", "", "", "")
	want := "https://macbook.example.ts.net/pair#pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
}

// A token is base64url today, so none of these characters occur in practice.
// The QR is still the wrong place to keep a latent encoding bug: a phone would
// redeem a token the desktop never issued, and the only symptom would be a
// pairing that fails for no visible reason.
//
// The expected value is also what the macOS app produces, so the two surfaces
// cannot drift into emitting different codes for the same token.
func TestMobilePairingURLEscapesTokensExactlyOnce(t *testing.T) {
	got := URL("mac.example.ts.net", 443, "a+b/c=d&e", "", "", "", "")
	want := "https://mac.example.ts.net/pair#pairing_token=a%2Bb%2Fc%3Dd%26e"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}

	// And it must survive the round trip the phone actually performs, which
	// reads the raw fragment rather than Go's decoded one.
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	values, err := url.ParseQuery(parsed.RawFragment)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	if token := values.Get("pairing_token"); token != "a+b/c=d&e" {
		t.Fatalf("token did not survive the round trip: %q", token)
	}
}

func TestMobilePairingURLIncludesNonDefaultHTTPSPort(t *testing.T) {
	got := URL("macbook-pro.tail2e5d9.ts.net", 8443, "one-time-token", "", "", "", "")
	want := "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
	if defaultPort := URL("macbook-pro.tail2e5d9.ts.net", 443, "token", "", "", "", ""); defaultPort != "https://macbook-pro.tail2e5d9.ts.net/pair#pairing_token=token" {
		t.Fatalf("default pairing URL = %q", defaultPort)
	}
}

func TestPairingURLCarriesTheEntitlement(t *testing.T) {
	got := URL(
		"desk.example.ts.net", 443, "pair-token",
		"https://relay.example.com", "desktop-key", "session-id", "entitlement-token",
	)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fields, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	if fields.Get("entitlement") != "entitlement-token" {
		t.Errorf("entitlement = %q, want the token; a relayed session cannot start without it",
			fields.Get("entitlement"))
	}
}

// Updated from the original "all four or none" rule: entitlement is now
// optional so that self-hosted relays (ALLOW_UNENTITLED=true) can still
// publish the three structural relay fields. The three structural fields
// (relay, key, session) still travel together or not at all; an empty
// entitlement is simply omitted rather than suppressing the whole group.
func TestPairingURLPublishesRelayFieldsEvenWithEmptyEntitlement(t *testing.T) {
	got := URL(
		"desk.example.ts.net", 443, "pair-token",
		"https://relay.example.com", "desktop-key", "session-id", "",
	)

	parsed, _ := url.Parse(got)
	fields, _ := url.ParseQuery(parsed.RawFragment)
	for _, key := range []string{"relay", "key", "session"} {
		if fields.Get(key) == "" {
			t.Errorf("%s is missing; the three structural relay fields must be published when non-empty", key)
		}
	}
	// Entitlement must be absent (not an empty value -- absent).
	if _, ok := fields["entitlement"]; ok {
		t.Errorf("entitlement should be omitted when empty, not published as empty string")
	}
	if fields.Get("pairing_token") != "pair-token" {
		t.Error("the pairing token must still be published; direct pairing does not need a relay")
	}
}

// A relay-only QR has the sentinel host "relay" so ParsePairingURL on the
// phone side knows there is no direct endpoint to try.
func TestMobilePairingURLRelayOnlyUsesRelayHost(t *testing.T) {
	got := URL(
		"relay", 443, "one-time-token",
		"https://redline-relay.example.com", "ZGVza3RvcC1rZXk=", "sess-abc", "",
	)
	// The QR must carry a valid URL with host "relay".
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Host != "relay" {
		t.Fatalf("host = %q, want \"relay\"", parsed.Host)
	}
	fragment, err := url.ParseQuery(parsed.RawFragment)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	if fragment.Get("relay") != "https://redline-relay.example.com" {
		t.Fatalf("relay = %q", fragment.Get("relay"))
	}
}

// A self-hosted relay (ALLOW_UNENTITLED=true) has no entitlement to carry,
// but the other three relay fields are still meaningful. They must be published
// so the phone can reach the relay even without an entitlement token.
func TestMobilePairingURLPublishesRelayWithoutEntitlement(t *testing.T) {
	got := URL(
		"desk.example.ts.net", 443, "pair-token",
		"https://my-relay.example.com", "desktop-key", "session-id", "",
	)
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fields, err := url.ParseQuery(parsed.RawFragment)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	// The three structural fields must appear.
	for _, key := range []string{"relay", "key", "session"} {
		if fields.Get(key) != "" {
			continue
		}
		t.Errorf("%s is missing; self-hosted relay needs relay+key+session even without an entitlement", key)
	}
	// Entitlement must be absent (not an empty value, absent).
	if _, ok := fields["entitlement"]; ok {
		t.Errorf("entitlement should be absent for self-hosted relay, got %q", fields.Get("entitlement"))
	}
}

// Compose on the desktop, parse on the phone, compare.
//
// The double-decode bug lived between two suites that each passed: the
// desktop's tests checked the URL it wrote, the phone's checked the URL it
// read, and neither fed one to the other. Every '+' in a base64 value was lost
// on the way across. This is the test that would have failed.
func TestWhatTheDesktopComposesIsWhatThePhoneReads(t *testing.T) {
	cases := map[string]struct {
		host, relay, key, session, entitlement string
	}{
		"tailnet and relay": {
			host: "macbook.example.ts.net", relay: "https://relay.example",
			key: "ds+l3Fu+I5pTwmwTna7cMnK+P4LZulXpQz7f+9v5+E=", session: "session-0123456789abcdefghijkl",
			entitlement: "eyJleHAiOjF9.T1jh+dnP/igZ0kpVoQFJ2+/==",
		},
		"relay only": {
			host: RelayOnlyHost, relay: "https://relay.example",
			key: "ds+l3Fu+I5pTwmwTna7cMnK+P4LZulXpQz7f+9v5+E=", session: "session-0123456789abcdefghijkl",
			entitlement: "eyJleHAiOjF9.T1jh+dnP/igZ0kpVoQFJ2+/==",
		},
		"relay only, self-hosted, no entitlement": {
			host: RelayOnlyHost, relay: "https://relay.example",
			key: "ds+l3Fu+I5pTwmwTna7cMnK+P4LZulXpQz7f+9v5+E=", session: "session-0123456789abcdefghijkl",
		},
		"tailnet only": {host: "macbook.example.ts.net"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			composed := URL(c.host, 8443, "one-time-token", c.relay, c.key, c.session, c.entitlement)

			parsed, err := core.ParsePairingURL(composed)
			if err != nil {
				t.Fatalf("the phone refused what the desktop composed: %v\n%s", err, composed)
			}
			var read struct {
				BaseURL     string `json:"base_url"`
				Token       string `json:"pairing_token"`
				Relay       string `json:"relay_url"`
				Key         string `json:"desktop_key"`
				Session     string `json:"relay_session"`
				Entitlement string `json:"entitlement_token"`
			}
			if err := json.Unmarshal([]byte(parsed), &read); err != nil {
				t.Fatal(err)
			}

			wantBase := "https://" + c.host + ":8443"
			if c.host == RelayOnlyHost {
				wantBase = ""
			}
			if read.BaseURL != wantBase {
				t.Errorf("base_url = %q, want %q", read.BaseURL, wantBase)
			}
			if read.Token != "one-time-token" {
				t.Errorf("token = %q", read.Token)
			}
			if read.Relay != c.relay || read.Key != c.key || read.Session != c.session || read.Entitlement != c.entitlement {
				t.Errorf("relay fields did not survive the trip:\n got  %q %q %q %q\n want %q %q %q %q",
					read.Relay, read.Key, read.Session, read.Entitlement,
					c.relay, c.key, c.session, c.entitlement)
			}
		})
	}
}
