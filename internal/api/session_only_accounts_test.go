package api_test

import (
	"net/http"
	"testing"

	"github.com/croutoncreations/redline/internal/config"
)

func TestManagedAccountSessionOnlyAPI(t *testing.T) {
	server, _, path := accountsServer(t, "")
	status, _, raw := accountRequest(t, http.MethodPost, server.URL+"/v1/accounts", map[string]any{"id": "team", "provider": "claude", "openusage_card": "claude@abcd1234", "weekly_policy": "session_only", "session_reserve": .6})
	if status != http.StatusCreated {
		t.Fatalf("status=%d %s", status, raw)
	}
	saved, err := config.ReadManagedAccounts(path)
	if err != nil || len(saved) != 1 || saved[0].WeeklyPolicy != "session_only" || saved[0].SessionReserve == nil || *saved[0].SessionReserve != .6 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	status, _, raw = accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/team", map[string]any{"weekly_policy": "", "session_reserve": 0})
	if status != http.StatusBadRequest {
		t.Fatalf("reserve without policy status=%d %s", status, raw)
	}
	status, _, raw = accountRequest(t, http.MethodPatch, server.URL+"/v1/accounts/team", map[string]any{"weekly_policy": ""})
	if status != http.StatusOK {
		t.Fatalf("clear policy status=%d %s", status, raw)
	}
}
