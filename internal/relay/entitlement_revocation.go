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
	"path/filepath"
	"sort"
)

const EntitlementRevocationSchemaVersion = 2

// EntitlementRevocation is a durable credential-bound set of revoked token
// authorities. It contains only one-way token hashes, never bearer plaintext.
type EntitlementRevocation struct {
	SchemaVersion         int      `json:"schema_version"`
	CredentialFingerprint string   `json:"credential_fingerprint"`
	RevokedTokenHashes    []string `json:"revoked_token_hashes"`
}

// EntitlementTokenHash identifies the exact bearer bytes without retaining the
// bearer credential itself.
func EntitlementTokenHash(token Secret) string {
	digest := sha256.Sum256([]byte(token.Value()))
	return hex.EncodeToString(digest[:])
}

func (marker EntitlementRevocation) Revokes(token Secret) bool {
	hash := EntitlementTokenHash(token)
	index := sort.SearchStrings(marker.RevokedTokenHashes, hash)
	return index < len(marker.RevokedTokenHashes) && marker.RevokedTokenHashes[index] == hash
}

func validTokenHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && hex.EncodeToString(decoded) == hash
}

func validateEntitlementRevocation(marker EntitlementRevocation) error {
	if marker.SchemaVersion != EntitlementRevocationSchemaVersion || marker.CredentialFingerprint == "" || len(marker.RevokedTokenHashes) == 0 {
		return errors.New("invalid entitlement revocation marker")
	}
	for index, hash := range marker.RevokedTokenHashes {
		if !validTokenHash(hash) || (index > 0 && marker.RevokedTokenHashes[index-1] >= hash) {
			return errors.New("invalid entitlement revocation token hashes")
		}
	}
	return nil
}

func canonicalRevocation(marker EntitlementRevocation) EntitlementRevocation {
	hashes := append([]string(nil), marker.RevokedTokenHashes...)
	sort.Strings(hashes)
	unique := hashes[:0]
	for _, hash := range hashes {
		if len(unique) == 0 || unique[len(unique)-1] != hash {
			unique = append(unique, hash)
		}
	}
	marker.RevokedTokenHashes = unique
	return marker
}

func decodeEntitlementRevocation(raw []byte) (EntitlementRevocation, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return EntitlementRevocation{}, fmt.Errorf("decode entitlement revocation: %w", err)
	}
	var encoded struct {
		SchemaVersion         *int      `json:"schema_version"`
		CredentialFingerprint *string   `json:"credential_fingerprint"`
		RevokedTokenHashes    *[]string `json:"revoked_token_hashes"`
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
	if encoded.SchemaVersion == nil || encoded.CredentialFingerprint == nil || encoded.RevokedTokenHashes == nil {
		return EntitlementRevocation{}, errors.New("decode entitlement revocation: missing or null field")
	}
	marker := EntitlementRevocation{
		SchemaVersion:         *encoded.SchemaVersion,
		CredentialFingerprint: *encoded.CredentialFingerprint,
		RevokedTokenHashes:    *encoded.RevokedTokenHashes,
	}
	if err := validateEntitlementRevocation(marker); err != nil {
		return EntitlementRevocation{}, err
	}
	return marker, nil
}

// EntitlementRevocationStore has its own path and process/file locks, so cache
// I/O cannot delay a terminal marker operation. Same-credential writes merge
// under the inter-process lock and therefore cannot drop an existing hash.
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
	marker = canonicalRevocation(marker)
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
		if existing.CredentialFingerprint == marker.CredentialFingerprint {
			marker.RevokedTokenHashes = append(marker.RevokedTokenHashes, existing.RevokedTokenHashes...)
			marker = canonicalRevocation(marker)
			if len(marker.RevokedTokenHashes) == len(existing.RevokedTokenHashes) {
				return op.syncDirectory()
			}
		}
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return op.write(append(raw, '\n'))
}
