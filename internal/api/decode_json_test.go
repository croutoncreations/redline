package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsContentHiddenBeyondSizeLimit(t *testing.T) {
	body := `{}` + strings.Repeat(" ", (1<<20)+1) + `{}`
	request := httptest.NewRequest("POST", "/", strings.NewReader(body))

	var target struct{}
	if err := decodeJSON(request, &target); err == nil || !strings.Contains(err.Error(), "request body exceeds") {
		t.Fatalf("decodeJSON oversized error = %v, want request body exceeds", err)
	}
}

func TestDecodeJSONAcceptsContentAtSizeLimit(t *testing.T) {
	body := `{}` + strings.Repeat(" ", (1<<20)-2)
	request := httptest.NewRequest("POST", "/", strings.NewReader(body))

	var target struct{}
	if err := decodeJSON(request, &target); err != nil {
		t.Fatalf("decodeJSON rejected a request at the size limit: %v", err)
	}
}
