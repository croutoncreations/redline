package store

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/croutoncreations/redline/internal/resetsignal"
)

func TestResetAnnouncementClaimsAreAtomicAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a := resetsignal.Announcement{Key: "announcement:123", Kind: resetsignal.KindCompleted}
	now := time.Now().UTC()
	if _, err := first.ObserveResetAnnouncement(t.Context(), a, now); err != nil {
		t.Fatal(err)
	}
	for _, retraction := range []bool{false, true} {
		var claims atomic.Int32
		var group sync.WaitGroup
		for i := range 12 {
			group.Add(1)
			go func() {
				defer group.Done()
				db := first
				if i%2 == 1 {
					db = second
				}
				var claimed bool
				var err error
				if retraction {
					claimed, err = db.ClaimResetAnnouncementRetraction(t.Context(), a.Key, now)
				} else {
					claimed, err = db.ClaimResetAnnouncement(t.Context(), a, now)
				}
				if err != nil {
					t.Error(err)
				}
				if claimed {
					claims.Add(1)
				}
			}()
		}
		group.Wait()
		if claims.Load() != 1 {
			t.Fatalf("retraction=%v claims=%d", retraction, claims.Load())
		}
	}
	// Even a previously unseen lower kind cannot be claimed after completion
	// (or after a retraction of the key).
	a.Kind = resetsignal.KindPromised
	if _, err := first.ObserveResetAnnouncement(t.Context(), a, now); err != nil {
		t.Fatal(err)
	}
	if claimed, err := first.ClaimResetAnnouncement(t.Context(), a, now); err != nil || claimed {
		t.Fatalf("downgrade claimed=%v err=%v", claimed, err)
	}
}
