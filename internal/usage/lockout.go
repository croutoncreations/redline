package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// lockoutFile persists provider rate-limit lockouts so a restart does not
// send an early request. Anthropic lengthens its usage-endpoint penalty when
// asked during it (462s grew to 3600s in one report), and restarting during
// development was enough to trigger that here.
type lockoutFile struct {
	Native       map[string]time.Time `json:"native,omitempty"`
	BankedResets map[string]time.Time `json:"banked_resets,omitempty"`
}

// SetLockoutPath enables persistence and restores any lockout still in force.
// Unreadable or missing files are ignored: persistence only makes the manager
// more cautious, never less.
func (m *Manager) SetLockoutPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockoutPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved lockoutFile
	if json.Unmarshal(data, &saved) != nil {
		return
	}
	now := m.Now().UTC()
	for key, until := range saved.Native {
		if until.After(now) {
			if m.nativeBlockedUntil == nil {
				m.nativeBlockedUntil = make(map[string]time.Time)
			}
			m.nativeBlockedUntil[key] = until
		}
	}
	for key, until := range saved.BankedResets {
		if until.After(now) {
			if m.resetStates == nil {
				m.resetStates = make(map[string]bankedResetState)
				m.accountProviders = make(map[string]string)
			}
			state := m.resetStates[key]
			state.nextFetch = until
			m.resetStates[key] = state
		}
	}
}

// saveLockoutsLocked writes current lockouts. Callers hold m.mu. Best effort:
// a failed write only loses caution across a restart.
func (m *Manager) saveLockoutsLocked() {
	if m.lockoutPath == "" {
		return
	}
	now := m.Now().UTC()
	saved := lockoutFile{Native: map[string]time.Time{}, BankedResets: map[string]time.Time{}}
	for key, until := range m.nativeBlockedUntil {
		if until.After(now) {
			saved.Native[key] = until
		}
	}
	for key, state := range m.resetStates {
		if state.lastError != "" && state.nextFetch.After(now) {
			saved.BankedResets[key] = state.nextFetch
		}
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.lockoutPath), ".usage-lockouts-")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), m.lockoutPath) != nil {
		_ = os.Remove(tmp.Name())
	}
}
