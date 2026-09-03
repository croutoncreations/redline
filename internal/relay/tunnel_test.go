package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The tunnel carries HTTP requests and responses as frames. These tests cover
// the encoding on its own, because a mistake here is a silently wrong response
// rather than a failure.

func TestRequestRoundTripsThroughAFrame(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v1/dispatch?task=build", strings.NewReader(`{"force":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")

	encoded, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.Method != http.MethodPost {
		t.Fatalf("method: %q", decoded.Method)
	}
	if decoded.Path != "/v1/dispatch?task=build" {
		t.Fatalf("path: %q", decoded.Path)
	}
	if decoded.Header.Get("Accept") != "application/json" {
		t.Fatalf("header lost: %v", decoded.Header)
	}
	if string(decoded.Body) != `{"force":true}` {
		t.Fatalf("body: %q", decoded.Body)
	}
}

func TestResponseRoundTripsThroughAFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.WriteHeader(http.StatusAccepted)
	rec.WriteString(`{"status":"started"}`)

	encoded, err := EncodeResponse(rec.Result())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// 202 and 200 mean different things in this API, so the status must
	// survive exactly.
	if decoded.Status != http.StatusAccepted {
		t.Fatalf("status: %d", decoded.Status)
	}
	if decoded.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type lost")
	}
	if string(decoded.Body) != `{"status":"started"}` {
		t.Fatalf("body: %q", decoded.Body)
	}
}

// Binary bodies (a log download, a future icon) must not be corrupted by the
// encoding, which is why bodies travel as bytes rather than as a string.
func TestBinaryBodySurvives(t *testing.T) {
	raw := []byte{0x00, 0xff, 0xfe, 0x41, 0x00, 0x80, 0x0a}
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	rec.Write(raw)

	encoded, err := EncodeResponse(rec.Result())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(decoded.Body) != string(raw) {
		t.Fatalf("binary body corrupted: %v", decoded.Body)
	}
}

func TestDecodeRejectsJunk(t *testing.T) {
	for _, bad := range [][]byte{nil, []byte(""), []byte("{"), []byte("not json")} {
		if _, err := DecodeRequest(bad); err == nil {
			t.Fatalf("decoded junk request %q", bad)
		}
		if _, err := DecodeResponse(bad); err == nil {
			t.Fatalf("decoded junk response %q", bad)
		}
	}
}

// A phone must not be able to reach outside the local API by asking for a
// path with a host in it, or by climbing out with traversal.
func TestTunnelledRequestsCannotEscapeTheLocalAPI(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("local"))
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())

	hostile := []string{
		"http://169.254.169.254/latest/meta-data",
		"https://example.com/steal",
		"//example.com/protocol-relative",
	}
	for _, path := range hostile {
		resp, err := forwarder.Forward(context.Background(), TunnelRequest{
			Method: http.MethodGet,
			Path:   path,
		})
		if err != nil {
			continue // refusing outright is fine
		}
		if resp.Status < 400 {
			t.Fatalf("request to %q was forwarded instead of refused (status %d)", path, resp.Status)
		}
	}
}

// The path from the phone is the tunnel's sharpest edge: it is
// attacker-controlled and it is about to be joined to a URL that can reach the
// desktop's own loopback services. Each of these is a real technique, kept as a
// table so a future change to the parser has to survive all of them.
func TestHostilePathsNeverReachAnythingButTheLocalAPI(t *testing.T) {
	var reached []string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())

	hostile := []string{
		"http://169.254.169.254/latest/meta-data", // cloud metadata
		"https://example.com/steal",
		"HTTP://EXAMPLE.COM/x",  // scheme case
		"http:/example.com/x",   // single slash
		"//example.com/x",       // protocol relative
		"///example.com/x",      // extra slash
		"/\\example.com/x",      // backslash confusion
		"\\\\example.com\\x",    // UNC style
		"/../../../etc/passwd",  // traversal
		"/v1/../../etc/passwd",  // traversal mid-path
		"/%2e%2e/%2e%2e/etc/pw", // encoded traversal
		"/v1/%2F%2Fexample.com", // encoded slashes
		"/v1/x#@example.com",    // fragment hiding a host
		"/v1/x\r\nX-Evil: 1",    // header injection
		"/v1/x\tsplit",          // control character
		"relative/path",         // not rooted
		"",                      // empty
	}

	for _, path := range hostile {
		resp, err := forwarder.Forward(context.Background(), TunnelRequest{
			Method: http.MethodGet,
			Path:   path,
		})
		if err != nil {
			continue // refusing outright is a fine outcome
		}
		if resp.Status < 400 {
			t.Errorf("hostile path %q was forwarded with status %d", path, resp.Status)
		}
	}

	// Nothing hostile may have reached the local server at all.
	for _, got := range reached {
		if strings.Contains(got, "..") || strings.Contains(got, "example.com") ||
			strings.Contains(got, "\\") || strings.Contains(got, "etc/pw") {
			t.Errorf("a hostile path reached the local API: %q", got)
		}
	}
}

// Percent-encoding is where a path can mean two things at once. These cases
// are not host escapes, so they are not SSRF, but each one made the desktop
// issue a different request than the phone sent -- which is its own bug and
// makes a log a poor record of what happened.
func TestAmbiguouslyEncodedPathsAreRefusedOrForwardedVerbatim(t *testing.T) {
	var seen []string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())

	// Encoded control characters pass a scan of the raw string untouched and
	// only become dangerous once decoded.
	for _, path := range []string{"/v1/x%0d%0ay", "/v1/runs%00", "/v1/a%09b"} {
		resp, err := forwarder.Forward(context.Background(), TunnelRequest{
			Method: http.MethodGet,
			Path:   path,
		})
		if err == nil && resp.Status < 400 {
			t.Errorf("encoded control character in %q was accepted", path)
		}
	}

	// Double encoding must reach the API exactly as written. Forwarding the
	// decoded form would turn %2561 into %61, a different request.
	before := len(seen)
	resp, err := forwarder.Forward(context.Background(), TunnelRequest{
		Method: http.MethodGet,
		Path:   "/v1/%2561",
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status == http.StatusOK && len(seen) > before {
		if got := seen[len(seen)-1]; got != "/v1/%2561" {
			t.Fatalf("double-encoded path was rewritten in transit: sent %q, server saw %q",
				"/v1/%2561", got)
		}
	}
}

// Ordinary paths must survive the hardening: over-strict validation that
// breaks real requests is its own kind of failure.
func TestOrdinaryPathsStillWork(t *testing.T) {
	var seen string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())
	for _, path := range []string{
		"/v1/dashboard",
		"/v1/dashboard?fields=providers,generated_at",
		"/v1/runs/01HX9K2M3N4P5Q6R7S8T9V0W1X/logs",
		"/v1/tasks/my-task-name",
		"/v1/dashboard?q=hello%20world",
	} {
		resp, err := forwarder.Forward(context.Background(), TunnelRequest{
			Method: http.MethodGet,
			Path:   path,
		})
		if err != nil {
			t.Fatalf("ordinary path %q was refused: %v", path, err)
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("ordinary path %q got status %d", path, resp.Status)
		}
		if seen != path {
			t.Fatalf("path altered in transit: sent %q, server saw %q", path, seen)
		}
	}
}

// The whole point of the tunnel: an ordinary request reaches the real local
// API and its response comes back intact.
func TestForwarderReachesTheLocalAPI(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dashboard" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())
	resp, err := forwarder.Forward(context.Background(), TunnelRequest{
		Method: http.MethodGet,
		Path:   "/v1/dashboard",
		Header: http.Header{"Authorization": []string{"Bearer token-123"}},
	})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status: %d", resp.Status)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body: %q", resp.Body)
	}
}

// A hung or slow local handler must not pin the tunnel open forever.
func TestForwarderRespectsContextCancellation(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer local.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	forwarder := NewForwarder(local.URL, local.Client())
	if _, err := forwarder.Forward(ctx, TunnelRequest{Method: http.MethodGet, Path: "/v1/slow"}); err == nil {
		t.Fatal("a cancelled request should fail rather than block")
	}
}

// A response body large enough to exhaust memory on a phone (or the DO's
// message limit) must be refused rather than streamed blindly.
func TestOversizedResponsesAreRefused(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Deliberately larger than the tunnel's per-frame ceiling.
		w.Write(make([]byte, maxTunnelBody+1024))
	}))
	defer local.Close()

	forwarder := NewForwarder(local.URL, local.Client())
	resp, err := forwarder.Forward(context.Background(), TunnelRequest{
		Method: http.MethodGet,
		Path:   "/v1/huge",
	})
	if err == nil && resp.Status < 400 {
		t.Fatal("an oversized response was accepted")
	}
}

// The frame format is a contract with the phone, so a change that breaks it
// should fail here rather than in the app.
func TestFrameFormatIsStable(t *testing.T) {
	encoded, err := EncodeRequest(httptest.NewRequest(http.MethodGet, "/v1/usage", nil))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("frames must be JSON: %v", err)
	}
	for _, key := range []string{"method", "path"} {
		if _, ok := shape[key]; !ok {
			t.Fatalf("frame is missing %q: %v", key, shape)
		}
	}
}
