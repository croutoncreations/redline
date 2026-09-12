package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const EntitlementCacheSchemaVersion = 3

type CachedEntitlement struct {
	SchemaVersion         int    `json:"schema_version"`
	CredentialFingerprint string `json:"credential_fingerprint"`
	Revoked               bool   `json:"revoked,omitempty"`
	RevokedAt             int64  `json:"revoked_at,omitempty"`
	Token                 Secret `json:"token,omitempty"`
	Exp                   int64  `json:"exp,omitempty"`
	ObtainedAt            int64  `json:"obtained_at,omitempty"`
	SID                   string `json:"sid,omitempty"`
	MaxClients            int    `json:"max_clients,omitempty"`
}

// CredentialFingerprint binds cached authority to one high-entropy license
// credential without retaining a reversible form of that credential.
func CredentialFingerprint(credential string) string {
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

func (c CachedEntitlement) ValidAt(sid string, now time.Time) bool {
	if c.SchemaVersion != EntitlementCacheSchemaVersion || c.Revoked || c.CredentialFingerprint == "" || c.SID != sid || c.ObtainedAt <= 0 || time.Unix(c.ObtainedAt, 0).After(now.Add(EntitlementClockSkew)) {
		return false
	}
	// Clock skew is accepted only while checking issuer lifetime coherence.
	// Runtime authority ends at the signed expiration because the relay closes
	// the session at that exact instant.
	if !time.Unix(c.Exp, 0).After(now) || c.Exp-c.ObtainedAt > int64((EntitlementLifetime+EntitlementClockSkew)/time.Second) {
		return false
	}
	if c.MaxClients < 1 || c.MaxClients > 25 {
		return false
	}
	return validateCachedToken(c) == nil
}

func validateCachedToken(c CachedEntitlement) error {
	return ValidateEntitlementAt(Entitlement{
		Token: c.Token, Exp: c.Exp, MaxClients: c.MaxClients, Seats: 1, SeatsUsed: 1,
	}, c.SID, time.Unix(c.ObtainedAt, 0))
}

type entitlementCacheJSON struct {
	SchemaVersion         *int    `json:"schema_version"`
	CredentialFingerprint *string `json:"credential_fingerprint"`
	Revoked               *bool   `json:"revoked,omitempty"`
	RevokedAt             *int64  `json:"revoked_at,omitempty"`
	Token                 *string `json:"token,omitempty"`
	Exp                   *int64  `json:"exp,omitempty"`
	ObtainedAt            *int64  `json:"obtained_at,omitempty"`
	SID                   *string `json:"sid,omitempty"`
	MaxClients            *int    `json:"max_clients,omitempty"`
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var consumeValue func() error
	consumeValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := consumeValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := consumeValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected closing JSON delimiter")
		}
	}
	if err := consumeValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func decodeEntitlementCache(raw []byte) (CachedEntitlement, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return CachedEntitlement{}, fmt.Errorf("decode entitlement cache: %w", err)
	}
	var record entitlementCacheJSON
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return CachedEntitlement{}, fmt.Errorf("decode entitlement cache: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CachedEntitlement{}, errors.New("decode entitlement cache: trailing data")
	}
	if record.SchemaVersion == nil || record.CredentialFingerprint == nil {
		return CachedEntitlement{}, errors.New("decode entitlement cache: missing version or credential fingerprint")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return CachedEntitlement{}, fmt.Errorf("decode entitlement cache shape: %w", err)
	}
	_, hasRevoked := fields["revoked"]
	_, hasRevokedAt := fields["revoked_at"]
	_, hasToken := fields["token"]
	_, hasExp := fields["exp"]
	_, hasObtainedAt := fields["obtained_at"]
	_, hasSID := fields["sid"]
	_, hasMaxClients := fields["max_clients"]
	cached := CachedEntitlement{
		SchemaVersion: *record.SchemaVersion, CredentialFingerprint: *record.CredentialFingerprint,
	}
	if hasRevoked {
		if record.Revoked == nil || !*record.Revoked || !hasRevokedAt || record.RevokedAt == nil || hasToken || hasExp || hasObtainedAt || hasSID || hasMaxClients {
			return CachedEntitlement{}, errors.New("decode entitlement cache: malformed revocation tombstone")
		}
		cached.Revoked = true
		cached.RevokedAt = *record.RevokedAt
	} else {
		if hasRevokedAt || !hasToken || record.Token == nil || !hasExp || record.Exp == nil || !hasObtainedAt || record.ObtainedAt == nil || !hasSID || record.SID == nil || !hasMaxClients || record.MaxClients == nil {
			return CachedEntitlement{}, errors.New("decode entitlement cache: malformed authority record")
		}
		cached.Token = NewSecret(*record.Token)
		cached.Exp = *record.Exp
		cached.ObtainedAt = *record.ObtainedAt
		cached.SID = *record.SID
		cached.MaxClients = *record.MaxClients
	}
	if err := validateEntitlementCacheRecord(cached); err != nil {
		return CachedEntitlement{}, fmt.Errorf("decode entitlement cache: %w", err)
	}
	return cached, nil
}

// decodeEntitlementCacheForReplacement accepts schema v3 records normally and
// recognizes only records that schema v2 itself could have written. Schema v2
// remains invalid for Load authority, but a valid issuer decision must be able
// to migrate it while malformed and unknown-future records stay nonreplaceable.
func decodeEntitlementCacheForReplacement(raw []byte) (CachedEntitlement, bool, error) {
	// Inspect member-name tokens before either struct or map decoding. Both of
	// those JSON interpretations silently collapse duplicate schema-v2 keys.
	if duplicateErr := rejectDuplicateJSONKeys(raw); duplicateErr != nil {
		return CachedEntitlement{}, false, fmt.Errorf("decode entitlement cache replacement: %w", duplicateErr)
	}
	cached, err := decodeEntitlementCache(raw)
	if err == nil {
		return cached, false, nil
	}

	var legacy struct {
		SchemaVersion         *int    `json:"schema_version"`
		CredentialFingerprint *string `json:"credential_fingerprint"`
		Revoked               *bool   `json:"revoked,omitempty"`
		Token                 *string `json:"token,omitempty"`
		Exp                   *int64  `json:"exp,omitempty"`
		ObtainedAt            *int64  `json:"obtained_at,omitempty"`
		SID                   *string `json:"sid,omitempty"`
		MaxClients            *int    `json:"max_clients,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&legacy); decodeErr != nil {
		return CachedEntitlement{}, false, err
	}
	var trailing any
	if decodeErr := decoder.Decode(&trailing); !errors.Is(decodeErr, io.EOF) {
		return CachedEntitlement{}, false, err
	}
	if legacy.SchemaVersion == nil || *legacy.SchemaVersion != 2 || legacy.CredentialFingerprint == nil || *legacy.CredentialFingerprint == "" {
		return CachedEntitlement{}, false, err
	}
	var fields map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &fields); decodeErr != nil {
		return CachedEntitlement{}, false, err
	}
	_, hasRevoked := fields["revoked"]
	_, hasToken := fields["token"]
	_, hasExp := fields["exp"]
	_, hasObtainedAt := fields["obtained_at"]
	_, hasSID := fields["sid"]
	_, hasMaxClients := fields["max_clients"]
	if hasRevoked {
		if legacy.Revoked == nil || !*legacy.Revoked || len(fields) != 3 || hasToken || hasExp || hasObtainedAt || hasSID || hasMaxClients {
			return CachedEntitlement{}, false, err
		}
		return CachedEntitlement{SchemaVersion: 2, CredentialFingerprint: *legacy.CredentialFingerprint, Revoked: true}, true, nil
	}
	if len(fields) != 7 || !hasToken || legacy.Token == nil || !hasExp || legacy.Exp == nil || !hasObtainedAt || legacy.ObtainedAt == nil || !hasSID || legacy.SID == nil || !hasMaxClients || legacy.MaxClients == nil {
		return CachedEntitlement{}, false, err
	}
	cached = CachedEntitlement{
		SchemaVersion: 2, CredentialFingerprint: *legacy.CredentialFingerprint,
		Token: NewSecret(*legacy.Token), Exp: *legacy.Exp, ObtainedAt: *legacy.ObtainedAt,
		SID: *legacy.SID, MaxClients: *legacy.MaxClients,
	}
	if validateCachedToken(cached) != nil || cached.Exp-cached.ObtainedAt > int64((EntitlementLifetime+EntitlementClockSkew)/time.Second) {
		return CachedEntitlement{}, false, err
	}
	return cached, true, nil
}

func validateEntitlementCacheRecord(cached CachedEntitlement) error {
	if cached.SchemaVersion != EntitlementCacheSchemaVersion || cached.CredentialFingerprint == "" {
		return errors.New("unversioned or unbound entitlement cache")
	}
	if cached.Revoked {
		if cached.RevokedAt <= 0 || cached.Token.Value() != "" || cached.Exp != 0 || cached.ObtainedAt != 0 || cached.SID != "" || cached.MaxClients != 0 {
			return errors.New("revocation tombstone contains authority or lacks a revocation time")
		}
		return nil
	}
	if cached.RevokedAt != 0 || validateCachedToken(cached) != nil || cached.Exp-cached.ObtainedAt > int64((EntitlementLifetime+EntitlementClockSkew)/time.Second) {
		return errors.New("invalid entitlement cache")
	}
	return nil
}

func encodeEntitlementCache(cached CachedEntitlement) ([]byte, error) {
	if cached.Revoked {
		return json.Marshal(struct {
			SchemaVersion         int    `json:"schema_version"`
			CredentialFingerprint string `json:"credential_fingerprint"`
			Revoked               bool   `json:"revoked"`
			RevokedAt             int64  `json:"revoked_at"`
		}{cached.SchemaVersion, cached.CredentialFingerprint, true, cached.RevokedAt})
	}
	return json.Marshal(struct {
		SchemaVersion         int    `json:"schema_version"`
		CredentialFingerprint string `json:"credential_fingerprint"`
		Token                 string `json:"token"`
		Exp                   int64  `json:"exp"`
		ObtainedAt            int64  `json:"obtained_at"`
		SID                   string `json:"sid"`
		MaxClients            int    `json:"max_clients"`
	}{cached.SchemaVersion, cached.CredentialFingerprint, cached.Token.Value(), cached.Exp, cached.ObtainedAt, cached.SID, cached.MaxClients})
}

func entitlementCacheReplaces(existing, incoming CachedEntitlement) bool {
	if existing.CredentialFingerprint != incoming.CredentialFingerprint {
		return true
	}
	existingVersion := existing.ObtainedAt
	if existing.Revoked {
		existingVersion = existing.RevokedAt
	}
	incomingVersion := incoming.ObtainedAt
	if incoming.Revoked {
		incomingVersion = incoming.RevokedAt
	}
	if incomingVersion != existingVersion {
		return incomingVersion > existingVersion
	}
	// At an equal issuer receipt second, terminal revocation wins over
	// authority. Equal records and authority-to-authority races are no-ops.
	return incoming.Revoked && !existing.Revoked
}

// EntitlementCacheStore uses the same descriptor-relative, no-follow,
// process-lock and durable atomic replacement rules as managed relay state.
type EntitlementCacheStore struct {
	path string
	lock entitlementCacheLock
}

type entitlementCacheLock chan struct{}

func (l entitlementCacheLock) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l:
		return nil
	}
}

func (l entitlementCacheLock) release() { l <- struct{}{} }

var (
	entitlementCacheProcessLocks  sync.Map
	entitlementCacheDirectorySync = (*os.File).Sync
)

func NewEntitlementCacheStore(path string) *EntitlementCacheStore {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	cleaned := filepath.Clean(absolute)
	created := make(entitlementCacheLock, 1)
	created <- struct{}{}
	lock, _ := entitlementCacheProcessLocks.LoadOrStore(cleaned, created)
	return &EntitlementCacheStore{path: cleaned, lock: lock.(entitlementCacheLock)}
}

func DefaultEntitlementCachePath(identityPath string) string {
	return filepath.Join(filepath.Dir(identityPath), "relay-entitlement.json")
}

func (s *EntitlementCacheStore) Load(sid, credentialFingerprint string, now time.Time) (CachedEntitlement, bool, error) {
	if err := s.lock.acquire(context.Background()); err != nil {
		return CachedEntitlement{}, false, err
	}
	defer s.lock.release()
	op, err := beginEntitlementCacheOperation(s.path)
	if err != nil {
		return CachedEntitlement{}, false, err
	}
	defer op.close()
	raw, exists, err := op.read()
	if err != nil || !exists {
		return CachedEntitlement{}, exists, err
	}
	cached, err := decodeEntitlementCache(raw)
	if err != nil {
		return CachedEntitlement{}, false, err
	}
	if cached.CredentialFingerprint != credentialFingerprint || !cached.ValidAt(sid, now) {
		return CachedEntitlement{}, false, errors.New("entitlement cache is revoked, expired, credential-mismatched, or malformed")
	}
	return cached, true, nil
}

func (s *EntitlementCacheStore) Save(cached CachedEntitlement) error {
	return s.SaveContext(context.Background(), cached)
}

// SaveContext persists an entitlement while allowing a controller-owned
// persistence worker to stop before entering blocked lock or I/O stages.
func (s *EntitlementCacheStore) SaveContext(ctx context.Context, cached CachedEntitlement) error {
	if err := validateEntitlementCacheRecord(cached); err != nil {
		return fmt.Errorf("refuse %w", err)
	}
	raw, err := encodeEntitlementCache(cached)
	if err != nil {
		return fmt.Errorf("encode entitlement cache: %w", err)
	}
	raw = append(raw, '\n')
	if err := s.lock.acquire(ctx); err != nil {
		return err
	}
	defer s.lock.release()
	op, err := beginEntitlementCacheOperationContext(ctx, s.path)
	if err != nil {
		return err
	}
	defer op.close()
	// Read and compare while holding both the process lock and the file lock.
	// This prevents an obsolete writer in another store or process from
	// replacing a terminal revocation that acquired the lock first.
	existingRaw, exists, err := op.read()
	if err != nil {
		return err
	}
	if exists {
		existing, legacy, err := decodeEntitlementCacheForReplacement(existingRaw)
		if err != nil {
			return err
		}
		if !legacy && !entitlementCacheReplaces(existing, cached) {
			// A prior rename may be visible even though its parent-directory sync
			// failed. Re-sync the directory before acknowledging an equal/newer
			// monotonic record as durable.
			return op.syncDirectory()
		}
	}
	return op.write(raw)
}
