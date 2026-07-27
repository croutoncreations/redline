package mcpserver

import (
	"strings"
	"testing"
	"unicode/utf8"
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
