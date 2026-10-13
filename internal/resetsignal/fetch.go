package resetsignal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout                = 5 * time.Second
	defaultMaxBody                = int64(256 << 10)
	defaultBackoff                = 100 * time.Millisecond
	codexResetMinimumReadInterval = time.Minute
)

type Fetcher struct {
	HTTP      *http.Client
	Timeout   time.Duration
	MaxBody   int64
	UserAgent string
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	// AllowHTTP is only for deterministic httptest servers.
	AllowHTTP bool
}

type fetchedDocument struct {
	Body     []byte
	Header   http.Header
	Metadata FetchMetadata
}

func (f Fetcher) get(ctx context.Context, feed, endpoint string) (fetchedDocument, *SourceError) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(f.AllowHTTP && parsed.Scheme == "http")) || parsed.User != nil {
		return fetchedDocument{}, sourceError(feed, "invalid_url", "source endpoint must be an absolute HTTPS URL", FetchMetadata{})
	}
	if f.HTTP == nil {
		f.HTTP = http.DefaultClient
	}
	if f.Timeout <= 0 {
		f.Timeout = defaultTimeout
	}
	if f.MaxBody <= 0 {
		f.MaxBody = defaultMaxBody
	}
	if f.Now == nil {
		f.Now = time.Now
	}
	if f.UserAgent == "" {
		f.UserAgent = "redline-reset-preview/dev (+https://github.com/croutoncreations/redline)"
	}
	if f.Sleep == nil {
		f.Sleep = sleepContext
	}

	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	metadata := FetchMetadata{}
	for attempt := 1; attempt <= 2; attempt++ {
		metadata.Attempts = attempt
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if requestErr != nil {
			return fetchedDocument{}, sourceError(feed, "request", requestErr.Error(), metadata)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", f.UserAgent)
		resp, requestErr := f.HTTP.Do(req)
		if requestErr != nil {
			if attempt == 1 && ctx.Err() == nil {
				metadata.BackoffApplied = retryFloor(feed)
				if canRetryWithinContext(ctx, metadata.BackoffApplied) {
					if err := f.Sleep(ctx, metadata.BackoffApplied); err == nil {
						continue
					}
				} else {
					return fetchedDocument{}, sourceError(feed, "retry_deferred", "transient fetch failure; retry delay exceeds bounded request budget", metadata)
				}
			}
			return fetchedDocument{}, sourceError(feed, "fetch", requestErr.Error(), metadata)
		}
		metadata.StatusCode = resp.StatusCode
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if retry && attempt == 1 {
			serverDelay, validRetryAfter := retryDelay(resp.Header.Get("Retry-After"), f.Now())
			if validRetryAfter {
				metadata.RetryAfter = serverDelay
			}
			delay := serverDelay
			if delay < retryFloor(feed) {
				delay = retryFloor(feed)
			}
			metadata.BackoffApplied = delay
			_ = resp.Body.Close()
			if !canRetryWithinContext(ctx, delay) {
				code := "retry_deferred"
				if resp.StatusCode == http.StatusTooManyRequests {
					code = "rate_limited"
				}
				return fetchedDocument{}, sourceError(feed, code, "source retry delay exceeds bounded request budget; no early retry was sent", metadata)
			}
			if err := f.Sleep(ctx, delay); err == nil {
				continue
			}
			code := "retry_deferred"
			if resp.StatusCode == http.StatusTooManyRequests {
				code = "rate_limited"
			}
			return fetchedDocument{}, sourceError(feed, code, "source retry wait was interrupted; no retry was sent", metadata)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			code := "http_status"
			if resp.StatusCode == http.StatusTooManyRequests {
				code = "rate_limited"
			}
			return fetchedDocument{}, sourceError(feed, code, fmt.Sprintf("source returned HTTP %d", resp.StatusCode), metadata)
		}
		mediaType, _, mediaErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if mediaErr != nil || mediaType != "application/json" {
			_ = resp.Body.Close()
			return fetchedDocument{}, sourceError(feed, "not_json", "source response is not application/json", metadata)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, f.MaxBody+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return fetchedDocument{}, sourceError(feed, "read", readErr.Error(), metadata)
		}
		if int64(len(body)) > f.MaxBody {
			return fetchedDocument{}, sourceError(feed, "oversized", fmt.Sprintf("source response exceeds %d bytes", f.MaxBody), metadata)
		}
		return fetchedDocument{Body: body, Header: resp.Header.Clone(), Metadata: metadata}, nil
	}
	return fetchedDocument{}, sourceError(feed, "fetch", "source retry budget exhausted", metadata)
}

func decodeOne(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func sourceError(feed, code, message string, metadata FetchMetadata) *SourceError {
	return &SourceError{Feed: feed, Code: code, Message: message, Metadata: metadata}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now), true
	}
	return 0, false
}

func retryFloor(feed string) time.Duration {
	if feed == "codex-reset" {
		return codexResetMinimumReadInterval
	}
	return defaultBackoff
}

func canRetryWithinContext(ctx context.Context, delay time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return delay > 0 && time.Until(deadline) > delay
}
