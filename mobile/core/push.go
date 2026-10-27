package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// RegisterPush associates this paired device with a push provider token.
func (c *Client) RegisterPush(platform, provider, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return c.do(ctx, http.MethodPut, "/v1/push/registration", map[string]string{
		"platform": platform, "provider": provider, "token": token,
	}, nil)
}

// UnregisterPush removes this paired device's push registration.
func (c *Client) UnregisterPush() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return c.do(ctx, http.MethodDelete, "/v1/push/registration", nil, nil)
}

type pushDelivery struct {
	ID        int64           `json:"id"`
	EventType string          `json:"event_type"`
	CreatedAt string          `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// pushContext leaves five seconds for a relay after a hung direct attempt,
// within the Android service/receiver's nine-second lifetime.
func pushContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	return context.WithValue(ctx, directAttemptTimeoutKey{}, 3*time.Second), cancel
}

// FetchNotificationsAfter returns an ascending page of deliveries as a JSON
// array, flattening the event payload into title, message and string data.
func (c *Client) FetchNotificationsAfter(afterID int64, limit int) (string, error) {
	ctx, cancel := pushContext()
	defer cancel()
	var deliveries []pushDelivery
	path := "/v1/notifications?after_id=" + strconv.FormatInt(afterID, 10) + "&limit=" + strconv.Itoa(limit)
	if err := c.get(ctx, path, &deliveries); err != nil {
		return "", err
	}
	type notification struct {
		ID        int64             `json:"id"`
		EventType string            `json:"event_type"`
		Title     string            `json:"title"`
		Message   string            `json:"message"`
		Data      map[string]string `json:"data"`
		CreatedAt string            `json:"created_at"`
	}
	items := make([]notification, 0, len(deliveries))
	for _, delivery := range deliveries {
		var item notification
		if len(delivery.Payload) > 0 {
			if err := json.Unmarshal(delivery.Payload, &item); err != nil {
				return "", fmt.Errorf("decode notification payload: %w", err)
			}
		}
		item.ID, item.EventType, item.CreatedAt = delivery.ID, delivery.EventType, delivery.CreatedAt
		if item.Data == nil {
			item.Data = map[string]string{}
		}
		items = append(items, item)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode notifications: %w", err)
	}
	return string(encoded), nil
}

// LatestNotificationID returns zero when the desktop has no deliveries.
func (c *Client) LatestNotificationID() (int64, error) {
	ctx, cancel := pushContext()
	defer cancel()
	var deliveries []pushDelivery
	if err := c.get(ctx, "/v1/notifications?limit=1", &deliveries); err != nil {
		return 0, err
	}
	if len(deliveries) == 0 {
		return 0, nil
	}
	return deliveries[0].ID, nil
}

// FiresaleAction starts or stops the firesale for an announcement.
func (c *Client) FiresaleAction(announcementKey, action string) error {
	ctx, cancel := pushContext()
	defer cancel()
	return c.do(ctx, http.MethodPost, "/v1/reset-signals/firesale", map[string]string{
		"announcement_key": announcementKey, "action": action,
	}, nil)
}
