package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestMobilePairingURLIncludesNonDefaultHTTPSPort(t *testing.T) {
	got := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 8443, "one-time-token")
	want := "https://macbook-pro.tail2e5d9.ts.net:8443/m?pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
	if defaultPort := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 443, "token"); defaultPort != "https://macbook-pro.tail2e5d9.ts.net/m?pairing_token=token" {
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
