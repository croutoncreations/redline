package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

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

// maxDeviceNameRunes bounds the unauthenticated, owner-visible device name.
const maxDeviceNameRunes = 100

// cleanDeviceName strips control and format characters, collapses whitespace,
// and caps the length of a name supplied by a pairing device.
func cleanDeviceName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	runes := []rune(strings.Join(strings.Fields(name), " "))
	if len(runes) > maxDeviceNameRunes {
		runes = runes[:maxDeviceNameRunes]
	}
	return string(runes)
}

func deviceTokenHash(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

// sessionIssuer fingerprints the current master token. Sessions record it at
// creation and only match while the same master token is loaded, so rotation
// revokes them all. An HMAC keeps the master token unrecoverable from the DB.
func (s *Server) sessionIssuer() string { return sessionIssuer(s.config.APIToken) }

func sessionIssuer(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("redline device session issuer v1"))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (s *Server) newDeviceSession(r *http.Request, name, kind string) (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", fmt.Errorf("generate device token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	at := s.now().UTC()
	session := store.DeviceSession{ID: uuid.NewString(), Name: name, Kind: kind, CreatedAt: at, LastUsedAt: at}
	if err := s.store.CreateDeviceSession(r.Context(), session, deviceTokenHash(token), s.sessionIssuer()); err != nil {
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
		if _, err := s.store.AuthenticateDeviceSession(r.Context(), deviceTokenHash(s.legacyDeviceToken), s.sessionIssuer(), s.now().UTC()); err == nil {
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
	id, err := s.store.AuthenticateDeviceSession(r.Context(), deviceTokenHash(token), s.sessionIssuer(), s.now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return principal{}, nil
	}
	if err != nil {
		return principal{}, err
	}
	return principal{deviceID: id, viaCookie: viaCookie, token: token}, nil
}

var errDeviceRevoked = errors.New("device session revoked")

// deviceSessionActive reports whether a device token still authenticates
// under the current master token. Long-lived responses call it periodically.
func (s *Server) deviceSessionActive(ctx context.Context, token string) bool {
	_, err := s.store.AuthenticateDeviceSession(ctx, deviceTokenHash(token), s.sessionIssuer(), s.now().UTC())
	return err == nil
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
