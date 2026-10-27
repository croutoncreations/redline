package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/croutoncreations/redline/internal/store"
	"github.com/google/uuid"
)

type principalContextKey struct{}

type principal struct {
	owner     bool
	deviceID  string
	viaCookie bool
	legacy    bool
	token     string
}

func (p principal) valid() bool { return p.owner || p.deviceID != "" || p.legacy }

func requestPrincipal(r *http.Request) principal {
	p, _ := r.Context().Value(principalContextKey{}).(principal)
	return p
}

func requireOwner(w http.ResponseWriter, r *http.Request) bool {
	if !requestPrincipal(r).owner {
		writeJSON(w, http.StatusForbidden, problem{Error: "this action requires the owner API bearer token"})
		return false
	}
	return true
}

func deviceTokenHash(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

func (s *Server) newDeviceSession(r *http.Request, name, kind string) (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("generate device token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	at := s.now().UTC()
	session := store.DeviceSession{ID: uuid.NewString(), Name: name, Kind: kind, CreatedAt: at, LastUsedAt: at}
	if err := s.store.CreateDeviceSession(r.Context(), session, deviceTokenHash(token)); err != nil {
		return "", "", err
	}
	return token, session.ID, nil
}

// legacyDeviceSession returns the one device session that every legacy
// master-token cookie migrates to in this process. A dashboard sends many
// requests with the old cookie before the new one arrives; they must share a
// session instead of each minting their own.
func (s *Server) legacyDeviceSession(r *http.Request) (string, string, error) {
	s.legacyDeviceMu.Lock()
	defer s.legacyDeviceMu.Unlock()
	if s.legacyDeviceToken != "" {
		if _, err := s.store.AuthenticateDeviceSession(r.Context(), deviceTokenHash(s.legacyDeviceToken), s.now().UTC()); err == nil {
			return s.legacyDeviceToken, s.legacyDeviceID, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", "", err
		}
		// Revoked since: mint a replacement below.
	}
	token, id, err := s.newDeviceSession(r, "Browser (migrated pairing)", "browser")
	if err != nil {
		return "", "", err
	}
	s.legacyDeviceToken, s.legacyDeviceID = token, id
	return token, id, nil
}

// Master bearers remain owner credentials for CLI/Mac/MCP and legacy phones.
// Master cookies are legacy devices and are upgraded by ServeHTTP.
func (s *Server) authorized(r *http.Request) (principal, error) {
	token := ""
	viaCookie := false
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(authorization, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	} else if cookie, err := r.Cookie(apiSessionCookie); err == nil {
		token, viaCookie = cookie.Value, true
	}
	if token == "" {
		return principal{}, nil
	}
	if secureTokenEqual(token, s.config.APIToken) {
		return principal{owner: !viaCookie, legacy: viaCookie, viaCookie: viaCookie, token: token}, nil
	}
	id, err := s.store.AuthenticateDeviceSession(r.Context(), deviceTokenHash(token), s.now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return principal{}, nil
	}
	if err != nil {
		return principal{}, err
	}
	return principal{deviceID: id, viaCookie: viaCookie, token: token}, nil
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	if !requireOwner(w, r) {
		return
	}
	devices, err := s.store.DeviceSessions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if !requireOwner(w, r) {
		return
	}
	if err := s.store.RevokeDeviceSession(r.Context(), r.PathValue("id"), s.now().UTC()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
