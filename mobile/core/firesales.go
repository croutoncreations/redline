package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// IsNotFound reports a typed desktop 404, including over the relay bridge.
func IsNotFound(err error) bool {
	var api *apiError
	return errors.As(err, &api) && api.StatusCode == http.StatusNotFound
}

// IsForbidden reports an owner-gated endpoint's refusal.
func IsForbidden(err error) bool {
	var api *apiError
	return errors.As(err, &api) && api.StatusCode == http.StatusForbidden
}

// ListFiresales returns the desktop's manual sales and eligible accounts unchanged.
func (c *Client) ListFiresales() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var payload json.RawMessage
	if err := c.get(ctx, "/v1/firesales", &payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

// StartFiresale starts an on-demand sale using the normal client deadline.
func (c *Client) StartFiresale(account, until string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/v1/firesales", map[string]string{"account": account, "until": until}, nil)
}

// StopFiresale stops an on-demand sale. A missing sale remains a typed 404.
func (c *Client) StopFiresale(account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return c.do(ctx, http.MethodDelete, "/v1/firesales/"+url.PathEscape(account), nil, nil)
}

// SendTestNotification asks the desktop to send a reset push to its devices.
func (c *Client) SendTestNotification() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/v1/notifications/test", map[string]any{"event": "reset.announced", "push": true}, nil)
}
