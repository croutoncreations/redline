package cli

import (
	"bytes"
	"strings"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
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
	// go-qrcode documents Bitmap as "bitmap[y][x] is true if the pixel at
	// (x, y) is set", i.e. true means a DARK module.
	bitmap := [][]bool{{true, false}, {false, true}}
	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	if !strings.Contains(output.String(), "  ▀▄  ") {
		t.Fatalf("rendered QR has incorrect module polarity:\n%s", output.String())
	}
}

// The previous version of this test asserted the opposite polarity using a
// hand-written bitmap, so it encoded the same wrong assumption as the code and
// passed while the rendered code was unscannable. These tests drive the real
// library instead, so the fixture cannot silently agree with the bug.

// go-qrcode includes a four-module quiet zone, so the first bitmap row is
// entirely light. Rendering it as ink means the polarity is inverted — the
// exact symptom that made the pairing QR unscannable.
func TestRenderTerminalQRLeavesQuietZoneBlank(t *testing.T) {
	code, err := qrcode.New("https://example.com/pair", qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	bitmap := code.Bitmap()
	for x, dark := range bitmap[0] {
		if dark {
			t.Fatalf("fixture invariant broken: bitmap[0][%d] is dark, expected quiet zone", x)
		}
	}

	var output bytes.Buffer
	renderTerminalQR(&output, bitmap)
	// Line 0 is the pre-loop margin; line 1 is y=-2 (above the bitmap); line 2
	// is y=0, the first row of real bitmap data.
	lines := strings.Split(output.String(), "\n")
	if len(lines) < 3 {
		t.Fatalf("rendered output has %d lines, want at least 3", len(lines))
	}
	if strings.ContainsAny(lines[2], "█▀▄") {
		t.Fatalf("quiet zone row rendered as ink, so polarity is inverted:\n%q", lines[2])
	}
}

// A correctly rendered QR is mostly light: the quiet zone alone is a wide
// blank border. If ink covers most of the output the code has been inverted,
// which is what an unscannable render looks like in aggregate.
func TestRenderTerminalQRIsNotMostlyInk(t *testing.T) {
	code, err := qrcode.New("https://example.com/pair", qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	renderTerminalQR(&output, code.Bitmap())

	var ink, total int
	for _, r := range output.String() {
		if r == '\n' {
			continue
		}
		total++
		if r == '█' || r == '▀' || r == '▄' {
			ink++
		}
	}
	if total == 0 {
		t.Fatal("rendered QR produced no cells")
	}
	if ink*2 >= total {
		t.Fatalf("rendered QR is %d/%d ink; an inverted code looks like this", ink, total)
	}
}
