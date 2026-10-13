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

const EntitlementRevocationSchemaVersion = 3

// EntitlementRevocation is an append-only ledger of credential-bound revoked
// token authorities. It contains only one-way token hashes, never bearer
// plaintext.
type EntitlementRevocation struct {
	SchemaVersion int                 `json:"schema_version"`
	Revocations   map[string][]string `json:"revocations"`
}

func NewEntitlementRevocation(fingerprint string, hashes ...string) EntitlementRevocation {
	return canonicalRevocation(EntitlementRevocation{
		SchemaVersion: EntitlementRevocationSchemaVersion,
		Revocations:   map[string][]string{fingerprint: append([]string(nil), hashes...)},
	})
}

// EntitlementTokenHash identifies the exact bearer bytes without retaining the
// bearer credential itself.
func EntitlementTokenHash(token Secret) string {
	digest := sha256.Sum256([]byte(token.Value()))
	return hex.EncodeToString(digest[:])
}

func (marker EntitlementRevocation) Revokes(fingerprint string, token Secret) bool {
	return marker.RevokesHash(fingerprint, EntitlementTokenHash(token))
}

func (marker EntitlementRevocation) RevokesHash(fingerprint, tokenHash string) bool {
	hashes := marker.Revocations[fingerprint]
	index := sort.SearchStrings(hashes, tokenHash)
	return index < len(hashes) && hashes[index] == tokenHash
}

func validTokenHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && hex.EncodeToString(decoded) == hash
}

func validateEntitlementRevocation(marker EntitlementRevocation) error {
	if marker.SchemaVersion != EntitlementRevocationSchemaVersion || len(marker.Revocations) == 0 {
		return errors.New("invalid entitlement revocation ledger")
	}
	for fingerprint, hashes := range marker.Revocations {
		if fingerprint == "" || len(hashes) == 0 {
			return errors.New("invalid entitlement revocation credential entry")
		}
		for index, hash := range hashes {
			if !validTokenHash(hash) || (index > 0 && hashes[index-1] >= hash) {
				return errors.New("invalid entitlement revocation token hashes")
			}
		}
	}
	return nil
}

func canonicalRevocation(marker EntitlementRevocation) EntitlementRevocation {
	canonical := EntitlementRevocation{
		SchemaVersion: marker.SchemaVersion,
		Revocations:   make(map[string][]string, len(marker.Revocations)),
	}
	for fingerprint, values := range marker.Revocations {
		hashes := append([]string(nil), values...)
		sort.Strings(hashes)
		unique := hashes[:0]
		for _, hash := range hashes {
			if len(unique) == 0 || unique[len(unique)-1] != hash {
				unique = append(unique, hash)
			}
		}
		canonical.Revocations[fingerprint] = unique
	}
	return canonical
}

func mergeRevocations(left, right EntitlementRevocation) EntitlementRevocation {
	merged := EntitlementRevocation{
		SchemaVersion: EntitlementRevocationSchemaVersion,
		Revocations:   make(map[string][]string, len(left.Revocations)+len(right.Revocations)),
	}
	for fingerprint, hashes := range left.Revocations {
		merged.Revocations[fingerprint] = append([]string(nil), hashes...)
	}
	for fingerprint, hashes := range right.Revocations {
		merged.Revocations[fingerprint] = append(merged.Revocations[fingerprint], hashes...)
	}
	return canonicalRevocation(merged)
}

func decodeEntitlementRevocation(raw []byte) (EntitlementRevocation, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return EntitlementRevocation{}, fmt.Errorf("decode entitlement revocation: %w", err)
	}
	var encoded struct {
		SchemaVersion *int                 `json:"schema_version"`
		Revocations   *map[string][]string `json:"revocations"`
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
	if encoded.SchemaVersion == nil || encoded.Revocations == nil {
		return EntitlementRevocation{}, errors.New("decode entitlement revocation: missing or null field")
	}
	marker := EntitlementRevocation{SchemaVersion: *encoded.SchemaVersion, Revocations: *encoded.Revocations}
	if err := validateEntitlementRevocation(marker); err != nil {
		return EntitlementRevocation{}, err
	}
	return marker, nil
}

// EntitlementRevocationStore has its own path and process/file locks, so cache
// I/O cannot delay a terminal ledger operation. All writes merge under the
// inter-process lock and therefore cannot drop any credential's existing hash.
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

// CommitIfUnrevoked holds both the process and inter-process ledger locks while
// checking one exact credential-bound token hash and running commit. The
// callback must only perform short in-memory publication; filesystem or network
// I/O would unnecessarily block revocation appends.
func (s *EntitlementRevocationStore) CommitIfUnrevoked(ctx context.Context, fingerprint, tokenHash string, commit func() bool) (EntitlementRevocation, bool, error) {
	if fingerprint == "" || !validTokenHash(tokenHash) || commit == nil {
		return EntitlementRevocation{}, false, errors.New("invalid guarded entitlement commit")
	}
	if err := s.lock.acquire(ctx); err != nil {
		return EntitlementRevocation{}, false, err
	}
	defer s.lock.release()
	op, err := beginEntitlementCacheOperationContext(ctx, s.path)
	if err != nil {
		return EntitlementRevocation{}, false, err
	}
	defer op.close()
	raw, exists, err := op.read()
	if err != nil {
		return EntitlementRevocation{}, false, err
	}
	var marker EntitlementRevocation
	if exists {
		marker, err = decodeEntitlementRevocation(raw)
		if err != nil {
			return EntitlementRevocation{}, false, err
		}
		if marker.RevokesHash(fingerprint, tokenHash) {
			return marker, false, nil
		}
	}
	return marker, commit(), nil
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
		merged := mergeRevocations(existing, marker)
		if revocationLedgersEqual(existing, merged) {
			return op.syncDirectory()
		}
		marker = merged
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return op.write(append(raw, '\n'))
}

func revocationLedgersEqual(left, right EntitlementRevocation) bool {
	if len(left.Revocations) != len(right.Revocations) {
		return false
	}
	for fingerprint, leftHashes := range left.Revocations {
		rightHashes, ok := right.Revocations[fingerprint]
		if !ok || len(leftHashes) != len(rightHashes) {
			return false
		}
		for index := range leftHashes {
			if leftHashes[index] != rightHashes[index] {
				return false
			}
		}
	}
	return true
}
