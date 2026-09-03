package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// maxTunnelBody is the largest response body we will forward in a single
// frame. A Durable Object WebSocket message is capped at 1 MB, but we size
// this larger so chunked responses can be assembled server-side without
// hitting the wire limit on every hop. The real ceiling is memory safety on
// the phone: a 4 MB frame is already larger than any normal API response
// and smaller than an embarrassing OOM.
const maxTunnelBody = 4 * 1024 * 1024

// TunnelRequest is an HTTP request encoded for transit through a Noise frame.
//
// The path is kept separate from the base URL because the phone must never
// be able to choose where the desktop dials — only which local endpoint to
// hit. Forwarder.Forward validates the path before constructing the real URL.
type TunnelRequest struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
}

// TunnelResponse carries the desktop's reply back through the tunnel.
//
// Status is kept as a number because 202 and 200 mean different things in
// this API (started vs held-back), and collapsing them to ok/err would hide
// that distinction from the phone.
type TunnelResponse struct {
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
}

// EncodeRequest serialises an outgoing HTTP request into a frame.
//
// The request's URL is stored as the raw RequestURI so query strings survive
// without double-encoding.
func EncodeRequest(r *http.Request) ([]byte, error) {
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, maxTunnelBody))
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}

	// RequestURI carries path + raw query without the host, which is what the
	// phone sets when it targets a specific endpoint.
	path := r.RequestURI
	if path == "" {
		path = r.URL.RequestURI()
	}

	return json.Marshal(TunnelRequest{
		Method: r.Method,
		Path:   path,
		Header: r.Header,
		Body:   body,
	})
}

// DecodeRequest deserialises a frame back into a TunnelRequest.
func DecodeRequest(frame []byte) (TunnelRequest, error) {
	if len(frame) == 0 {
		return TunnelRequest{}, errors.New("empty frame")
	}
	var req TunnelRequest
	if err := json.Unmarshal(frame, &req); err != nil {
		return TunnelRequest{}, fmt.Errorf("decode request frame: %w", err)
	}
	if req.Method == "" {
		return TunnelRequest{}, errors.New("frame is missing method")
	}
	return req, nil
}

// EncodeResponse serialises the desktop's HTTP response into a frame.
//
// Bodies are read with a limit so a runaway local handler cannot fill the
// tunnel buffer. A response larger than maxTunnelBody is an error; the
// caller should return 502 rather than trying to stream.
func EncodeResponse(r *http.Response) ([]byte, error) {
	limited := io.LimitReader(r.Body, maxTunnelBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > maxTunnelBody {
		return nil, fmt.Errorf("response body exceeds the %d-byte tunnel limit", maxTunnelBody)
	}

	return json.Marshal(TunnelResponse{
		Status: r.StatusCode,
		Header: r.Header,
		Body:   body,
	})
}

// DecodeResponse deserialises a frame back into a TunnelResponse.
func DecodeResponse(frame []byte) (TunnelResponse, error) {
	if len(frame) == 0 {
		return TunnelResponse{}, errors.New("empty frame")
	}
	var resp TunnelResponse
	if err := json.Unmarshal(frame, &resp); err != nil {
		return TunnelResponse{}, fmt.Errorf("decode response frame: %w", err)
	}
	return resp, nil
}

// Forwarder replays tunnelled requests against the desktop's local API.
//
// The baseURL is the only destination the forwarder will ever reach. A path
// that tries to escape it — by carrying a host, a scheme, or traversal
// sequences — is refused before any network call is made.
type Forwarder struct {
	baseURL string
	client  *http.Client
}

// NewForwarder creates a Forwarder that replays requests against baseURL.
// Passing a custom client lets tests substitute a test server's client
// without opening the production socket.
func NewForwarder(baseURL string, client *http.Client) *Forwarder {
	if client == nil {
		client = http.DefaultClient
	}
	return &Forwarder{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
	}
}

// Forward replays a tunnelled request against the local API and returns the
// response. It refuses any path that tries to leave the local API, because
// the path comes from the phone and must be treated as attacker-controlled.
func (f *Forwarder) Forward(ctx context.Context, req TunnelRequest) (TunnelResponse, error) {
	safePath, err := resolvePath(req.Path)
	if err != nil {
		// Return a 400 rather than an error so the phone gets a legible
		// response rather than a closed frame. The caller should log the
		// reason; we do not want to echo it to the phone.
		return TunnelResponse{Status: http.StatusBadRequest}, nil
	}

	target := f.baseURL + safePath

	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}

	outbound, err := http.NewRequestWithContext(ctx, req.Method, target, bodyReader)
	if err != nil {
		return TunnelResponse{}, fmt.Errorf("build request: %w", err)
	}
	for key, vals := range req.Header {
		for _, v := range vals {
			outbound.Header.Add(key, v)
		}
	}

	res, err := f.client.Do(outbound)
	if err != nil {
		return TunnelResponse{}, fmt.Errorf("forward: %w", err)
	}
	defer res.Body.Close()

	limited := io.LimitReader(res.Body, maxTunnelBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return TunnelResponse{}, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > maxTunnelBody {
		return TunnelResponse{}, fmt.Errorf("response body exceeds the %d-byte tunnel limit", maxTunnelBody)
	}

	return TunnelResponse{
		Status: res.StatusCode,
		Header: res.Header,
		Body:   body,
	}, nil
}

// resolvePath validates and normalises a path received from the phone.
//
// The path arrives from an untrusted caller, so we must not allow it to name
// any host other than the desktop's local API. Three classes of attack are
// refused here:
//
//  1. Absolute URLs ("http://..." or "https://...") — the scheme gives the
//     game away immediately.
//  2. Protocol-relative paths ("//host/x") — url.Parse treats the leading
//     "//" as an authority, yielding a URL with a non-empty Host. We detect
//     that after parsing rather than pattern-matching, so the check is robust
//     to encoding variations.
//  3. Paths that would climb above the root via ".." segments — cleaned by
//     url.Parse / path.Clean combined. Anything that still starts with ".."
//     after cleaning is rejected.
//
// A rejected path returns an error; the caller maps that to a 400.
func resolvePath(raw string) (string, error) {
	// This path arrives from the phone, through a relay we do not trust, and is
	// about to be joined to a URL that can reach the desktop's own loopback
	// services. It is the one place in the tunnel where a mistake turns the
	// desktop into a proxy for the network it sits on, so the rule is an
	// allowlist: anything not obviously an ordinary local API path is refused.

	// Control characters would also enable request smuggling on the way out.
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("path contains a control character")
		}
	}
	// Backslashes are path separators to some parsers and not to others, and
	// that disagreement is what makes them useful to an attacker.
	if strings.ContainsAny(raw, "\\") {
		return "", fmt.Errorf("path contains a backslash")
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("path must be rooted")
	}
	if strings.HasPrefix(raw, "//") {
		return "", fmt.Errorf("path carries an authority")
	}
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("path contains a scheme")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	// A non-empty Host means the path carried an authority. url.Parse is the
	// authoritative parser, so this catches encodings a string check misses.
	if parsed.Host != "" || parsed.Scheme != "" || parsed.User != nil {
		return "", fmt.Errorf("path would escape the local API")
	}

	// Traversal is checked on the DECODED path, because %2e%2e is how it
	// arrives when someone is actually trying. path.Clean then collapses any
	// remaining sequences so nothing downstream has to.
	if strings.Contains(parsed.Path, "..") {
		return "", fmt.Errorf("path contains a traversal sequence")
	}
	// An encoded separator (%2F) decodes into a path segment boundary that the
	// sender took care to hide. Whatever the intent, the decoded and undecoded
	// forms address different things, and a request whose meaning depends on
	// which parser reads it is one to refuse rather than resolve.
	if parsed.Path != parsed.EscapedPath() && strings.Contains(parsed.Path, "/") {
		if decoded := strings.Count(parsed.Path, "/"); decoded != strings.Count(parsed.EscapedPath(), "/") {
			return "", fmt.Errorf("path contains an encoded separator")
		}
	}
	cleaned := path.Clean(parsed.Path)
	if !strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "//") {
		return "", fmt.Errorf("path is not a safe local path")
	}
	// path.Clean collapsing the path means it addressed something other than
	// what was written; forwarding the tidied version would silently change the
	// request.
	if cleaned != parsed.Path {
		return "", fmt.Errorf("path is not already in canonical form")
	}

	// A fragment is meaningless to a server and is a known way to hide a host
	// from a naive parser. Refuse rather than silently strip it, so a request
	// that meant something odd does not quietly become a request that means
	// something ordinary.
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("path contains a fragment")
	}

	// Rebuild from the validated parts.
	out := cleaned
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out, nil
}
