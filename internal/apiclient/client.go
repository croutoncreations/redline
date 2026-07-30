package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func (c Client) Do(ctx context.Context, method, path string, body, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s %s: encode request body: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(
		ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader,
	)
	if err != nil {
		return fmt.Errorf("%s %s: build request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&problem)
		if problem.Error == "" {
			problem.Error = resp.Status
		}
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, problem.Error)
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}
