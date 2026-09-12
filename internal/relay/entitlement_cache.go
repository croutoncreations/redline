package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"
)

type CachedEntitlement struct {
	Token      Secret `json:"token"`
	Exp        int64  `json:"exp"`
	ObtainedAt int64  `json:"obtained_at"`
	SID        string `json:"sid"`
	MaxClients int    `json:"max_clients"`
}

func (c CachedEntitlement) ValidAt(sid string, now time.Time) bool {
	if c.SID != sid || c.ObtainedAt <= 0 || c.ObtainedAt > now.Unix() {
		return false
	}
	if c.Exp <= now.Unix() || c.Exp-c.ObtainedAt > int64(EntitlementLifetime/time.Second) {
		return false
	}
	if c.MaxClients < 1 || c.MaxClients > 25 {
		return false
	}
	return validateCachedToken(c) == nil
}

func validateCachedToken(c CachedEntitlement) error {
	return validateEntitlement(Entitlement{
		Token: c.Token, Exp: c.Exp, MaxClients: c.MaxClients, Seats: 1, SeatsUsed: 1,
	}, c.SID, time.Unix(c.ObtainedAt, 0))
}

// EntitlementCacheStore uses the same descriptor-relative, no-follow,
// process-lock and durable atomic replacement rules as managed relay state.
type EntitlementCacheStore struct {
	path string
	mu   *sync.Mutex
}

var entitlementCacheProcessLocks sync.Map

func NewEntitlementCacheStore(path string) *EntitlementCacheStore {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	cleaned := filepath.Clean(absolute)
	lock, _ := entitlementCacheProcessLocks.LoadOrStore(cleaned, &sync.Mutex{})
	return &EntitlementCacheStore{path: cleaned, mu: lock.(*sync.Mutex)}
}

func DefaultEntitlementCachePath(identityPath string) string {
	return filepath.Join(filepath.Dir(identityPath), "relay-entitlement.json")
}

func (s *EntitlementCacheStore) Load(sid string, now time.Time) (CachedEntitlement, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := beginEntitlementCacheOperation(s.path)
	if err != nil {
		return CachedEntitlement{}, false, err
	}
	defer op.close()
	raw, exists, err := op.read()
	if err != nil || !exists {
		return CachedEntitlement{}, exists, err
	}
	var cached CachedEntitlement
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cached); err != nil {
		return CachedEntitlement{}, false, fmt.Errorf("decode entitlement cache: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CachedEntitlement{}, false, errors.New("decode entitlement cache: trailing data")
	}
	if !cached.ValidAt(sid, now) {
		return CachedEntitlement{}, false, errors.New("entitlement cache is expired, mismatched, or malformed")
	}
	return cached, true, nil
}

func (s *EntitlementCacheStore) Save(cached CachedEntitlement) error {
	if validateCachedToken(cached) != nil || cached.Exp-cached.ObtainedAt > int64(EntitlementLifetime/time.Second) {
		return errors.New("refuse invalid entitlement cache")
	}
	raw, err := json.Marshal(cached)
	if err != nil {
		return fmt.Errorf("encode entitlement cache: %w", err)
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := beginEntitlementCacheOperation(s.path)
	if err != nil {
		return err
	}
	defer op.close()
	// Inspect an existing target through the protected descriptor before
	// replacement. In particular, never turn a planted symlink or
	// over-permissive cache into an apparently successful renewal.
	if _, _, err := op.read(); err != nil {
		return err
	}
	return op.write(raw)
}
