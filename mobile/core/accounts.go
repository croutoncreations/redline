package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Account management for the phone. The phone can see every account, rename
// one, switch it off or on, link an OpenUsage card the Mac already has, and
// remove an account Redline manages. It cannot sign in: that happens on the
// Mac in the provider's own CLI or in OpenUsage, and the screens say so.
// No credential ever crosses this boundary in either direction.

// AccountView is one provider account on the accounts screen.
type AccountView struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	OpenUsageCard string `json:"openusage_card,omitempty"`
	// Reads says in words where the numbers come from.
	Reads string `json:"reads"`
	// Editable is false for accounts in the Mac's config file, which are
	// changed by editing that file.
	Editable bool   `json:"editable"`
	Disabled bool   `json:"disabled"`
	Running  bool   `json:"running"`
	Pending  string `json:"pending,omitempty"`
	// State is one short phrase for the row ("Up to date", "Signed out",
	// "Saved · restart Redline to apply"), and Tone the colour to give it:
	// good, warn, danger or muted.
	State     string `json:"state"`
	Tone      string `json:"tone"`
	LastError string `json:"last_error,omitempty"`
	SignIn    string `json:"sign_in"`
}

// AccountsView is the whole accounts screen.
type AccountsView struct {
	Accounts        []AccountView `json:"accounts"`
	RestartRequired bool          `json:"restart_required"`
}

type accountsPayload struct {
	Accounts []struct {
		ID            string `json:"id"`
		Provider      string `json:"provider"`
		Label         string `json:"label"`
		UsageSource   string `json:"usage_source"`
		OpenUsageCard string `json:"openusage_card"`
		Origin        string `json:"origin"`
		Disabled      bool   `json:"disabled"`
		Running       bool   `json:"running"`
		Pending       string `json:"pending"`
		Status        string `json:"status"`
		LastError     string `json:"last_error"`
		SignIn        string `json:"sign_in"`
	} `json:"accounts"`
	RestartRequired bool `json:"restart_required"`
}

func renderAccounts(payload accountsPayload) AccountsView {
	view := AccountsView{Accounts: make([]AccountView, 0, len(payload.Accounts)), RestartRequired: payload.RestartRequired}
	for _, item := range payload.Accounts {
		account := AccountView{
			ID: item.ID, Provider: item.Provider, Label: item.Label, OpenUsageCard: item.OpenUsageCard,
			Editable: item.Origin == "managed", Disabled: item.Disabled, Running: item.Running, Pending: item.Pending,
			LastError: item.LastError, SignIn: item.SignIn,
		}
		if account.Label == "" {
			account.Label = item.ID
		}
		account.Reads = "Default " + displayProvider(item.Provider) + " login on the Mac"
		if item.OpenUsageCard != "" && !strings.EqualFold(item.OpenUsageCard, item.Provider) {
			account.Reads = "OpenUsage card " + item.OpenUsageCard
		} else if strings.EqualFold(item.OpenUsageCard, item.Provider) {
			account.Reads = "OpenUsage default " + displayProvider(item.Provider) + " card"
		}
		account.State, account.Tone = accountState(item.Pending, item.Disabled, item.Status)
		view.Accounts = append(view.Accounts, account)
	}
	return view
}

func displayProvider(provider string) string {
	switch strings.ToLower(provider) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	default:
		return provider
	}
}

func accountState(pending string, disabled bool, status string) (string, string) {
	switch pending {
	case "add", "enable", "change":
		return "Saved · restart Redline on the Mac to apply", "warn"
	case "disable":
		return "Turning off · restart Redline on the Mac to apply", "warn"
	case "remove":
		return "Removing · restart Redline on the Mac to apply", "warn"
	}
	if disabled {
		return "Off", "muted"
	}
	switch status {
	case "ok":
		return "Up to date", "good"
	case "stale":
		return "Out of date", "warn"
	case "signed_out":
		return "Signed out", "danger"
	case "error":
		return "Can't read usage", "danger"
	case "no_data":
		return "Waiting for first reading", "muted"
	default:
		return "Unknown", "muted"
	}
}

// FetchAccounts returns the accounts screen as JSON.
func (c *Client) FetchAccounts() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var payload accountsPayload
	if err := c.get(ctx, "/v1/accounts", &payload); err != nil {
		return "", err
	}
	return encodeJSON(renderAccounts(payload))
}

// DiscoveredCard is an OpenUsage card the Mac can see.
type DiscoveredCard struct {
	ID            string    `json:"id"`
	Plan          string    `json:"plan,omitempty"`
	Stale         bool      `json:"stale"`
	HasWeekly     bool      `json:"has_weekly"`
	Error         string    `json:"error,omitempty"`
	FetchedAt     time.Time `json:"fetched_at,omitempty"`
	LinkedAccount string    `json:"linked_account,omitempty"`
}

// DiscoverView lists the cards available to link.
type DiscoverView struct {
	Provider string           `json:"provider"`
	Cards    []DiscoveredCard `json:"cards"`
	Error    string           `json:"error,omitempty"`
	Hint     string           `json:"hint"`
}

// DiscoverAccounts lists the OpenUsage cards the Mac has for a provider.
func (c *Client) DiscoverAccounts(provider string) (string, error) {
	if provider != "claude" && provider != "codex" {
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var view DiscoverView
	if err := c.get(ctx, "/v1/accounts/discover/"+url.PathEscape(provider), &view); err != nil {
		return "", err
	}
	if view.Cards == nil {
		view.Cards = []DiscoveredCard{}
	}
	return encodeJSON(view)
}

// AddAccount links an OpenUsage card the Mac already reads to a new account.
// Only the ID, provider, label and card cross; nothing that signs in.
func (c *Client) AddAccount(id, provider, label, card string) (string, error) {
	id, card = strings.TrimSpace(id), strings.ToLower(strings.TrimSpace(card))
	if id == "" || card == "" {
		return "", errors.New("account id and card are required")
	}
	body := map[string]any{"id": id, "provider": provider, "label": strings.TrimSpace(label),
		"openusage_card": card, "usage_source": "openusage"}
	return c.accountChange("POST", "/v1/accounts", body)
}

// RenameAccount changes an account's display label.
func (c *Client) RenameAccount(id, label string) (string, error) {
	return c.accountChange("PATCH", "/v1/accounts/"+url.PathEscape(id), map[string]any{"label": strings.TrimSpace(label)})
}

// SetAccountDisabled switches an account off or back on.
func (c *Client) SetAccountDisabled(id string, disabled bool) (string, error) {
	return c.accountChange("PATCH", "/v1/accounts/"+url.PathEscape(id), map[string]any{"disabled": disabled})
}

// RemoveAccount forgets an account Redline manages. The desktop refuses while
// task profiles still use it.
func (c *Client) RemoveAccount(id string) (string, error) {
	return c.accountChange("DELETE", "/v1/accounts/"+url.PathEscape(id), nil)
}

func (c *Client) accountChange(method, path string, body any) (string, error) {
	if strings.TrimSpace(path) == "/v1/accounts/" {
		return "", errors.New("account id is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var payload accountsPayload
	if err := c.do(ctx, method, path, body, &payload); err != nil {
		return "", err
	}
	return encodeJSON(renderAccounts(payload))
}

func encodeJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(encoded), nil
}
