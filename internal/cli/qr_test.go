package cli

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
)

// A QR from a desktop with the relay enabled has to carry everything the phone
// needs to reach it from outside: where the relay is, and which public key
// identifies this desktop. Without the key there is nothing to authenticate the
// far end of a relayed session against.
func TestMobilePairingURLCarriesRelayDetails(t *testing.T) {
	got := mobilePairingURL(
		"macbook.example.ts.net", 443, "one-time-token",
		"https://relay.example.com", "ZGVza3RvcC1rZXk=", "session-abcdefghij0123",
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
	got := mobilePairingURL("macbook.example.ts.net", 443, "one-time-token", "", "", "")
	want := "https://macbook.example.ts.net/pair#pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
}

func TestMobilePairingURLIncludesNonDefaultHTTPSPort(t *testing.T) {
	got := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 8443, "one-time-token", "", "", "")
	want := "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
	if defaultPort := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 443, "token", "", "", ""); defaultPort != "https://macbook-pro.tail2e5d9.ts.net/pair#pairing_token=token" {
		t.Fatalf("default pairing URL = %q", defaultPort)
	}
}

func TestRenderTerminalQRUsesFalseBitmapCellsAsDarkModules(t *testing.T) {
	// skip2/go-qrcode represents white modules as true and dark modules as false.
	bitmap := [][]bool{{false, true}, {true, false}}
	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	if !strings.Contains(output.String(), "  ▀▄  ") {
		t.Fatalf("rendered QR has incorrect module polarity:\n%s", output.String())
	}
}
