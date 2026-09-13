package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A QR from a desktop with the relay enabled has to carry everything the phone
// needs to reach it from outside: where the relay is, and which public key
// identifies this desktop. Without the key there is nothing to authenticate the
// far end of a relayed session against.
func TestRenderTerminalQRUsesFalseBitmapCellsAsDarkModules(t *testing.T) {
	// skip2/go-qrcode represents white modules as true and dark modules as false.
	bitmap := [][]bool{{false, true}, {true, false}}
	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	if !strings.Contains(output.String(), "  ▀▄  ") {
		t.Fatalf("rendered QR has incorrect module polarity:\n%s", output.String())
	}
}

// The QR carries the relay address, the desktop key and the session id, but
// not the entitlement -- so a phone that fell back to the relay dialled a
// closed relay with an empty token and got 402. Relay fallback could not work
// at all, and the app reported plain unreachability.
//
// All four travel together for the same reason the other three do: any one
// missing leaves the phone unable to complete a relayed session.
