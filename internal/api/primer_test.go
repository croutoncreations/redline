package api_test

import (
	"net/http"
	"testing"

	"github.com/croutoncreations/redline/internal/domain"
)

type primerStatusForTest struct {
	Settings   domain.PrimerSettings `json:"settings"`
	Configured bool                  `json:"configured"`
	Supported  bool                  `json:"supported"`
	NextPingAt *string               `json:"next_ping_at"`
	Forecast   []map[string]string   `json:"forecast"`
}

func TestPrimerSettingsAPIReturnsDefaultsAndAppliesPartialUpdates(t *testing.T) {
	t.Parallel()
	server, _ := newAPIServer(t, claudePayload)
	var initial primerStatusForTest
	getJSON(t, server.URL+"/v1/providers/claude-main/primer", &initial)
	if initial.Configured || initial.Settings.Enabled || !initial.Supported ||
		initial.Settings.Mode != domain.PrimerSchedule || initial.Settings.Model != "haiku" {
		t.Fatalf("defaults=%#v", initial)
	}

	var updated primerStatusForTest
	patchJSON(t, server.URL+"/v1/providers/claude-main/primer", map[string]any{
		"enabled": true, "times": []string{"11:00", "6:00"}, "days": []string{"weekdays"}, "timezone": "America/Chicago",
	}, &updated)
	if !updated.Configured || !updated.Settings.Enabled || len(updated.Settings.Times) != 2 ||
		updated.Settings.Times[0] != "06:00" || len(updated.Settings.Days) != 5 || updated.NextPingAt == nil ||
		len(updated.Forecast) == 0 {
		t.Fatalf("updated=%#v", updated)
	}

	patchJSON(t, server.URL+"/v1/providers/claude-main/primer", map[string]any{"mode": "reset"}, &updated)
	if updated.Settings.Mode != domain.PrimerReset || updated.Settings.Timezone != "America/Chicago" ||
		len(updated.Settings.Times) != 2 {
		t.Fatalf("a partial update must keep other fields: %#v", updated)
	}
}

func TestPrimerAPIRejectsInvalidSettingsAndUnsupportedProviders(t *testing.T) {
	t.Parallel()
	server, _ := newAPIServer(t, claudePayload)
	for _, body := range []string{
		`{"mode":"often"}`,
		`{"times":["26:00"]}`,
		`{"timezone":"Nowhere/Land"}`,
		`{"enabled":true,"times":[]}`,
		`{"unknown":true}`,
		`{"model":"--dangerously-skip-permissions"}`,
	} {
		requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/claude-main/primer", body, http.StatusBadRequest)
	}
	requestStatus(t, http.MethodPatch, server.URL+"/v1/providers/codex-main/primer", `{"enabled":true}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/providers/codex-main/primer/run", `{}`, http.StatusBadRequest)
	requestStatus(t, http.MethodPost, server.URL+"/v1/providers/claude-main/primer/run?force=maybe", `{}`, http.StatusBadRequest)
	requestStatus(t, http.MethodGet, server.URL+"/v1/providers/claude-main/primer/history?limit=x", ``, http.StatusBadRequest)
	requestStatus(t, http.MethodGet, server.URL+"/v1/providers/missing/primer", ``, http.StatusNotFound)
	requestStatus(t, http.MethodGet, server.URL+"/v1/providers/missing/primer/history", ``, http.StatusNotFound)
}

func TestPrimerManualRunSkipsWhenAWindowIsOpen(t *testing.T) {
	t.Parallel()
	// The fixture reports a session window resetting two hours after apiNow,
	// so an unforced ping must be skipped without invoking Claude.
	server, _ := newAPIServer(t, claudePayload)
	attempt := postJSON[domain.PrimerAttempt](t, server.URL+"/v1/providers/claude-main/primer/run", map[string]any{})
	if attempt.Outcome != domain.PrimerSkipped || attempt.WindowResetsAt == nil {
		t.Fatalf("attempt=%#v", attempt)
	}
	var history []domain.PrimerAttempt
	getJSON(t, server.URL+"/v1/providers/claude-main/primer/history", &history)
	if len(history) != 1 || history[0].Trigger != "manual" {
		t.Fatalf("history=%#v", history)
	}
}

func TestDashboardIncludesPrimerOnlyForSupportedProviders(t *testing.T) {
	t.Parallel()
	server, _ := newAPIServer(t, claudePayload)
	var dashboard struct {
		Providers []struct {
			ID     string               `json:"id"`
			Primer *primerStatusForTest `json:"primer"`
		} `json:"providers"`
	}
	getJSON(t, server.URL+"/v1/dashboard", &dashboard)
	for _, provider := range dashboard.Providers {
		if (provider.ID == "claude-main") != (provider.Primer != nil) {
			t.Fatalf("provider %s primer=%#v", provider.ID, provider.Primer)
		}
	}
}
