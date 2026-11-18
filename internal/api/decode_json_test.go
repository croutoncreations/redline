package api

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingJSONReader struct{}

func (failingJSONReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

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

func TestDecodePairingJSONPreservesCustomLimitErrors(t *testing.T) {
	t.Run("too large", func(t *testing.T) {
		body := `{}` + strings.Repeat(" ", 16<<10)
		request := httptest.NewRequest("POST", "/", strings.NewReader(body))

		var target struct{}
		err := decodePairingJSON(request, &target)
		if err == nil || err.Error() != "invalid JSON: request is too large" {
			t.Fatalf("decodePairingJSON oversized error = %v", err)
		}
	})

	t.Run("read failure", func(t *testing.T) {
		request := httptest.NewRequest("POST", "/", nil)
		request.Body = io.NopCloser(failingJSONReader{})

		var target struct{}
		err := decodePairingJSON(request, &target)
		if err == nil || err.Error() != "invalid JSON: could not read request" {
			t.Fatalf("decodePairingJSON read error = %v", err)
		}
	})
}
