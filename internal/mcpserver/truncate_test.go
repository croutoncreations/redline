package mcpserver

import (
	"strings"
	"testing"
	"time"
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

func TestViewRunEventDoesNotSplitMultiByteRuneInPayload(t *testing.T) {
	// Build a JSON payload whose raw bytes place a 3-byte rune ('€')
	// straddling the maxEventPayload truncation boundary: the header
	// `{"output":"` plus filler puts '€' one byte before the cut, so
	// slicing at maxEventPayload includes only its first byte.
	header := `{"output":"`
	filler := strings.Repeat("a", maxEventPayload-1-len(header))
	payload := []byte(header + filler + `€rest"}`)
	if len(payload) <= maxEventPayload {
		t.Fatalf("test setup invalid: payload not larger than limit")
	}
	if utf8.RuneStart(payload[maxEventPayload]) {
		t.Fatalf("test setup invalid: cut byte is not mid-rune")
	}

	event := domain.RunEvent{
		ID: 1, RunID: "run-1", Type: "harness.completed",
		OccurredAt: time.Now(), Payload: payload,
	}

	view := viewRunEvent(event)

	if !view.PayloadTruncated {
		t.Fatalf("expected payload to be reported as truncated")
	}
	text, ok := view.Payload.(string)
	if !ok {
		t.Fatalf("expected truncated payload to be a string, got %T", view.Payload)
	}
	if !utf8.ValidString(text) {
		t.Fatalf("viewRunEvent produced invalid UTF-8: %q (bytes %v)", text, []byte(text))
	}
}
