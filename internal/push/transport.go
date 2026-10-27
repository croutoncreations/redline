// Package push sends content-free wake hints through an optional transport.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var ErrNotConfigured = errors.New("push not configured")
var ErrUnregistered = errors.New("push registration unregistered")
var errRelay = errors.New("push relay request failed")

type WakeHint struct {
	DeliveryID int64  `json:"delivery_id"`
	Kind       string `json:"kind"`
}
type Wake struct {
	Provider string   `json:"provider"`
	Token    string   `json:"token"`
	Platform string   `json:"platform"`
	Hint     WakeHint `json:"hint"`
}
type Transport interface {
	Wake(context.Context, Wake) error
}
type Disabled struct{}

func (Disabled) Wake(context.Context, Wake) error { return ErrNotConfigured }

// RelayTransport deliberately never includes response bodies, URLs, or HTTP
// errors in returned errors: any of them could reflect tokens or credentials.
type RelayTransport struct {
	BaseURL          string
	AuthToken        string
	mu               sync.Mutex
	unavailableUntil time.Time
}

func (t *RelayTransport) Wake(ctx context.Context, wake Wake) error {
	t.mu.Lock()
	unavailable := time.Now().Before(t.unavailableUntil)
	t.mu.Unlock()
	if unavailable {
		return ErrNotConfigured
	}
	payload, err := json.Marshal(wake)
	if err != nil {
		return errRelay
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/v1/push", bytes.NewReader(payload))
	if err != nil {
		return errRelay
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.AuthToken)
	// Never forward a shared secret or device token through a redirect.
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errRelay
	}
	defer response.Body.Close()
	if response.StatusCode == 404 || response.StatusCode == 501 {
		t.mu.Lock()
		t.unavailableUntil = time.Now().Add(time.Hour)
		t.mu.Unlock()
		return ErrNotConfigured
	}
	if response.StatusCode == 410 {
		return ErrUnregistered
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	var body struct {
		Code  string          `json:"code"`
		Error json.RawMessage `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&body) == nil {
		var code string
		_ = json.Unmarshal(body.Error, &code)
		var nested struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body.Error, &nested)
		if body.Code == "unregistered" || code == "unregistered" || nested.Code == "unregistered" {
			return ErrUnregistered
		}
	}
	return errRelay
}
