package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// apiSessionCookie is the cookie the service sets when a pairing token is
// redeemed. Its value is the durable API token.
const apiSessionCookie = "redline_api_session"

// PairingRequest is what a scanned Redline QR code contains.
type PairingRequest struct {
	BaseURL      string `json:"base_url"`
	PairingToken string `json:"pairing_token"`
}

// ParsePairingURL reads a scanned QR code and returns the pairing details as
// JSON.
//
// The format is the one `redline pair --qr` already produces, so the desktop
// needs no changes and the phone pairs exactly as the web dashboard does. The
// token travels in the URL fragment, which browsers never send to a server;
// that matters less here but keeping the format identical means one thing to
// maintain.
//
// A camera will happily scan any code it is pointed at, so anything that is not
// a Redline pairing URL is rejected with a reason rather than producing a
// client aimed at nothing.
func ParsePairingURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("this does not look like a Redline pairing code")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("this does not look like a Redline pairing code")
	}
	if parsed.Host == "" || parsed.Path != "/pair" {
		return "", errors.New("this does not look like a Redline pairing code")
	}

	// Plain HTTP is only safe to loopback, where there is no network to
	// eavesdrop. Anywhere else it would put the credential on the wire.
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("pairing over plain HTTP is only allowed to this device")
	}

	token := url.Values{}
	if fragment := parsed.Fragment; fragment != "" {
		if values, err := url.ParseQuery(fragment); err == nil {
			token = values
		}
	}
	pairingToken := token.Get("pairing_token")
	if pairingToken == "" {
		return "", errors.New("this pairing code is incomplete; generate a new one")
	}

	base := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	encoded, err := json.Marshal(PairingRequest{
		BaseURL:      strings.TrimRight(base.String(), "/"),
		PairingToken: pairingToken,
	})
	if err != nil {
		return "", fmt.Errorf("encode pairing request: %w", err)
	}
	return string(encoded), nil
}

// EncodePairingQR renders a pairing URL as a QR bitmap, returned row by row as
// a JSON array of strings of '1' and '0'.
//
// This exists so an on-device test can feed the real decoder the same QR the
// desktop renders, rather than trusting that a hand-built fixture resembles
// one. gomobile cannot return a 2D array, hence the encoded form.
func EncodePairingQR(contents string) (string, error) {
	code, err := qrcode.New(contents, qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("encode pairing QR: %w", err)
	}
	bitmap := code.Bitmap()
	rows := make([]string, 0, len(bitmap))
	for _, row := range bitmap {
		var builder strings.Builder
		builder.Grow(len(row))
		for _, dark := range row {
			if dark {
				builder.WriteByte('1')
			} else {
				builder.WriteByte('0')
			}
		}
		rows = append(rows, builder.String())
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("encode pairing QR: %w", err)
	}
	return string(encoded), nil
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

// CreatePairingToken asks the service for a one-time pairing token, the same
// call `redline pair` makes.
//
// This exists for tests and for a future in-app "pair another device" flow: an
// already-paired device can mint a code for a new one without returning to the
// terminal.
func CreatePairingToken(baseURL, apiToken string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var response struct {
		Token string `json:"pairing_token"`
	}
	client := NewClient(baseURL, apiToken)
	if _, err := client.postNoBody(ctx, "/v1/pairing", &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("the service returned an empty pairing token")
	}
	return response.Token, nil
}

// RedeemPairing exchanges a one-time pairing token for the durable credential.
//
// The token is single-use and short-lived, so this can only succeed once per
// QR code. The service answers with the credential as a session cookie, which
// is the same exchange the web dashboard performs.
func RedeemPairing(baseURL, pairingToken string) (string, error) {
	if strings.TrimSpace(pairingToken) == "" {
		return "", errors.New("pairing token is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// No stored credential exists yet, so this deliberately builds a client
	// with an empty token: redeeming is how the credential is obtained.
	client := NewClient(baseURL, "")
	body := map[string]string{"pairing_token": pairingToken}

	response, err := client.doCapturingResponse(ctx, http.MethodPost, "/v1/pairing/redeem", body)
	if err != nil {
		return "", err
	}

	for _, cookie := range response.Cookies() {
		if cookie.Name == apiSessionCookie && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	// Storing an empty credential would look like an authorization failure
	// later, with nothing to explain why.
	return "", errors.New("the desktop accepted pairing but returned no credential")
}
