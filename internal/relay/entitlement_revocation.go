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

// EntitlementRevocation is a credential-bound terminal authority decision. It
// deliberately contains neither a credential nor an entitlement token.
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
// I/O cannot delay a terminal marker operation.
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
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
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
	}
	return op.write(raw)
}

func (s *EntitlementRevocationStore) ClearContext(ctx context.Context, credentialFingerprint string) error {
	if credentialFingerprint == "" {
		return errors.New("empty entitlement credential fingerprint")
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
	raw, exists, err := op.read()
	if err != nil {
		return err
	}
	if !exists {
		return op.syncDirectory()
	}
	marker, err := decodeEntitlementRevocation(raw)
	if err != nil {
		return err
	}
	if marker.CredentialFingerprint != credentialFingerprint {
		return nil
	}
	if err := op.remove(); err != nil {
		// Keep an immediately restarted process fail-closed when deletion could
		// not be acknowledged as crash durable.
		if restoreErr := op.write(raw); restoreErr != nil {
			return fmt.Errorf("clear entitlement revocation: %v; restore marker: %w", err, restoreErr)
		}
		return err
	}
	return nil
}
