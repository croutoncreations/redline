package mcpserver

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jfox/redline/internal/domain"
)

func TestViewRunEventDoesNotSplitMultiByteRuneOnTruncation(t *testing.T) {
	prefix := strings.Repeat("a", maxEventPayload-1) // 8191 ASCII bytes
	payload := prefix + "€rest"                      // '€' is a 3-byte rune straddling the maxEventPayload boundary

	event := domain.RunEvent{Payload: []byte(payload)}
	view := viewRunEvent(event)

	if !view.PayloadTruncated {
		t.Fatalf("expected payload to be reported as truncated")
	}
	truncated, ok := view.Payload.(string)
	if !ok {
		t.Fatalf("expected truncated payload to be a string, got %T", view.Payload)
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("viewRunEvent produced invalid UTF-8: %q (bytes %v)", truncated, []byte(truncated))
	}
}
