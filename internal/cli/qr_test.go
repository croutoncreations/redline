package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/skip2/go-qrcode"
)

func TestMobilePairingURLIncludesNonDefaultHTTPSPort(t *testing.T) {
	got := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 8443, "one-time-token")
	want := "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=one-time-token"
	if got != want {
		t.Fatalf("pairing URL = %q, want %q", got, want)
	}
	if defaultPort := mobilePairingURL("macbook-pro.tail2e5d9.ts.net", 443, "token"); defaultPort != "https://macbook-pro.tail2e5d9.ts.net/pair#pairing_token=token" {
		t.Fatalf("default pairing URL = %q", defaultPort)
	}
}

func TestRenderTerminalQRUsesTrueBitmapCellsAsDarkModules(t *testing.T) {
	// skip2/go-qrcode represents dark modules as true and light modules as false
	// (see (*qrcode.QRCode).Bitmap: "bitmap[y][x] is true if the pixel is set").
	bitmap := [][]bool{{true, false}, {false, true}}
	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	if !strings.Contains(output.String(), "  ▀▄  ") {
		t.Fatalf("rendered QR has incorrect module polarity:\n%s", output.String())
	}
}

// go-qrcode's four-module quiet zone means bitmap row 0 is false (light) across
// the entire width, so the rendered line for that row must contain no ink glyphs.
// This exercises the real library rather than a hand-crafted bitmap, since a
// hand-crafted bitmap can encode the same wrong polarity assumption as the code
// under test.
func TestRenderTerminalQRQuietZoneRowStaysBlank(t *testing.T) {
	code, err := qrcode.New("https://example.com/pair", qrcode.Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bitmap := code.Bitmap()
	for _, v := range bitmap[0] {
		if v {
			t.Fatalf("test assumption invalid: bitmap row 0 is not entirely light")
		}
	}
	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	lines := strings.Split(output.String(), "\n")
	// line 0: pre-loop margin blank; line 1: y=-2 (out of range, blank);
	// line 2: y=0, the first row of real bitmap data (the quiet zone row).
	quietZoneLine := lines[2]
	if strings.ContainsAny(quietZoneLine, "█▀▄") {
		t.Fatalf("quiet zone row rendered as ink; renderTerminalQR has inverted polarity:\n%q", quietZoneLine)
	}
}
