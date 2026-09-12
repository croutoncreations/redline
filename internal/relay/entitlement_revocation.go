package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

const EntitlementRevocationSchemaVersion = 1

// EntitlementRevocation is a durable credential-bound authority version floor.
// It deliberately contains neither a credential nor an entitlement token.
type EntitlementRevocation struct {
	SchemaVersion         int    `json:"schema_version"`
	CredentialFingerprint string `json:"credential_fingerprint"`
	RevokedAt             int64  `json:"revoked_at"`
}

func validateEntitlementRevocation(marker EntitlementRevocation) error {
	if marker.SchemaVersion != EntitlementRevocationSchemaVersion || marker.CredentialFingerprint == "" || marker.RevokedAt <= 0 {
		return errors.New("invalid entitlement revocation marker")
	}
	return nil
}

func decodeEntitlementRevocation(raw []byte) (EntitlementRevocation, error) {
	var encoded struct {
		SchemaVersion         *int    `json:"schema_version"`
		CredentialFingerprint *string `json:"credential_fingerprint"`
		RevokedAt             *int64  `json:"revoked_at"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return EntitlementRevocation{}, fmt.Errorf("decode entitlement revocation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return EntitlementRevocation{}, errors.New("decode entitlement revocation: trailing data")
	}
	if encoded.SchemaVersion == nil || encoded.CredentialFingerprint == nil || encoded.RevokedAt == nil {
		return EntitlementRevocation{}, errors.New("decode entitlement revocation: missing or null field")
	}
	marker := EntitlementRevocation{SchemaVersion: *encoded.SchemaVersion, CredentialFingerprint: *encoded.CredentialFingerprint, RevokedAt: *encoded.RevokedAt}
	if err := validateEntitlementRevocation(marker); err != nil {
		return EntitlementRevocation{}, err
	}
	return marker, nil
}

// EntitlementRevocationStore has its own path and process/file locks, so cache
// I/O cannot delay a terminal marker operation and writes can never lower the
// stored time floor.
type EntitlementRevocationStore struct {
	path string
	lock entitlementCacheLock
}

func NewEntitlementRevocationStore(path string) *EntitlementRevocationStore {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	cleaned := filepath.Clean(absolute)
	created := make(entitlementCacheLock, 1)
	created <- struct{}{}
	lock, _ := entitlementCacheProcessLocks.LoadOrStore(cleaned, created)
	return &EntitlementRevocationStore{path: cleaned, lock: lock.(entitlementCacheLock)}
}

func DefaultEntitlementRevocationPath(cachePath string) string {
	return filepath.Join(filepath.Dir(cachePath), "relay-entitlement-revocation.json")
}

func (s *EntitlementRevocationStore) Load() (EntitlementRevocation, bool, error) {
	if err := s.lock.acquire(context.Background()); err != nil {
		return EntitlementRevocation{}, false, err
	}
	defer s.lock.release()
	op, err := beginEntitlementCacheOperation(s.path)
	if err != nil {
		return EntitlementRevocation{}, false, err
	}
	defer op.close()
	raw, exists, err := op.read()
	if err != nil || !exists {
		return EntitlementRevocation{}, exists, err
	}
	marker, err := decodeEntitlementRevocation(raw)
	if err != nil {
		return EntitlementRevocation{}, false, err
	}
	return marker, true, nil
}

func (s *EntitlementRevocationStore) SaveContext(ctx context.Context, marker EntitlementRevocation) error {
	if err := validateEntitlementRevocation(marker); err != nil {
		return err
	}
	if err := s.lock.acquire(ctx); err != nil {
		return err
	}
	defer s.lock.release()
	op, err := beginEntitlementCacheOperationContext(ctx, s.path)
	if err != nil {
		return err
	}
	defer op.close()
	existingRaw, exists, err := op.read()
	if err != nil {
		return err
	}
	if exists {
		existing, err := decodeEntitlementRevocation(existingRaw)
		if err != nil {
			return err
		}
		if existing.CredentialFingerprint == marker.CredentialFingerprint && existing.RevokedAt >= marker.RevokedAt {
			return op.syncDirectory()
		}
		// Credential replacement changes which floor is relevant, but must not
		// let clock rollback lower the store's inter-process version watermark.
		if existing.RevokedAt > marker.RevokedAt {
			marker.RevokedAt = existing.RevokedAt
		}
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return op.write(append(raw, '\n'))
}
