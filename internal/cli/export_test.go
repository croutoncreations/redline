package cli

import (
	"net/http"
	"testing"
)

// UseConsoleForTest points the Claude Console organization lookup at a test
// server for the rest of the test.
func UseConsoleForTest(t testing.TB, baseURL string, client *http.Client) {
	t.Helper()
	previous := consoleClient
	consoleClient.BaseURL, consoleClient.HTTPClient = baseURL, client
	t.Cleanup(func() { consoleClient = previous })
}
