package mcpserver

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jfox/redline/internal/domain"
)

func TestTruncateStringDoesNotSplitMultiByteRune(t *testing.T) {
	limit := 10
	prefix := strings.Repeat("a", limit-1) // 9 ASCII bytes
	value := prefix + "€rest"              // '€' is a 3-byte rune starting at byte offset 9

	truncated, wasTruncated := truncateString(value, limit)

	if !wasTruncated {
		t.Fatalf("expected truncation to be reported")
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncateString produced invalid UTF-8: %q (bytes %v)", truncated, []byte(truncated))
	}
}

func TestViewRunEventDoesNotSplitMultiByteRune(t *testing.T) {
	// Not valid JSON, so viewRunEvent falls back to raw byte truncation.
	prefix := strings.Repeat("a", maxEventPayload-1) // fills bytes up to the limit
	payload := []byte(prefix + "€rest")              // '€' is a 3-byte rune straddling the byte-8192 boundary

	view := viewRunEvent(domain.RunEvent{Payload: payload})

	if !view.PayloadTruncated {
		t.Fatalf("expected payload to be reported as truncated")
	}
	truncated, ok := view.Payload.(string)
	if !ok {
		t.Fatalf("expected payload to be a string, got %T", view.Payload)
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("viewRunEvent produced invalid UTF-8: %q (bytes %v)", truncated, []byte(truncated))
	}
}
