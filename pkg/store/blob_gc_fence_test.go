package store

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestBlobGCCollectionRequiresCurrentFenceProperty(t *testing.T) {
	for _, inventory := range []string{"empty", "grace", "ready"} {
		t.Run(inventory, func(t *testing.T) {
			s := authorityStore(t)
			now := time.Unix(1_800_000_000, 0).UTC()
			s.clock = func() time.Time { return now }
			op := s.AsOperator()
			lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "collector", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if inventory != "empty" {
				want = 1
				marked := now
				if inventory == "ready" {
					marked = now.Add(-limits.BlobGCGrace)
				}
				digest := strings.Repeat("a", 64)
				rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", digest)
				rewriteSchemaExec(t, s.db, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES ('a',?,?)", digest, marked.UnixNano())
			}
			calls := 0
			deleter := blobDeleteFunc(func(context.Context, blob.Blob) error {
				calls++
				return errors.New("unauthorized physical deletion")
			})
			property := func(suffix string, generation uint32, field uint8) bool {
				forged := lease
				forged.ExpiresAt = now.Add(time.Hour)
				switch field % 5 {
				case 0:
					forged.Name += "other_" + suffix
				case 1:
					forged.Holder += "other_" + suffix
				case 2:
					forged.Generation += int64(generation) + 1
				case 3:
					forged.Generation = 0
				case 4:
					forged = SingletonLease{}
				}
				removed, err := op.CollectBlobTombstones(t.Context(), forged, 10, deleter)
				return removed == 0 && errors.Is(err, ErrLockHeld) && calls == 0
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 96, Rand: rand.New(rand.NewSource(891))}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaCount(t, s.db, "blobs", want)
			rewriteSchemaCount(t, s.db, "blob_tombstones", want)
		})
	}
}

func TestBlobGCEmptyCollectionRejectsExpiryAndTakeover(t *testing.T) {
	s := authorityStore(t)
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	op := s.AsOperator()
	lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "collector", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deleter := blobDeleteFunc(func(context.Context, blob.Blob) error {
		t.Fatal("empty collection attempted physical deletion")
		return nil
	})
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, deleter); n != 0 || err != nil {
		t.Fatalf("live empty pass=%d: %v", n, err)
	}
	now = lease.ExpiresAt
	lease.ExpiresAt = now.Add(time.Hour)
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, deleter); n != 0 || !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expired empty pass=%d: %v", n, err)
	}
	replacement, err := op.AcquireSingletonLease(t.Context(), "maintenance", lease.Holder, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := op.CollectBlobTombstones(t.Context(), lease, 10, deleter); n != 0 || !errors.Is(err, ErrLockHeld) {
		t.Fatalf("superseded empty pass=%d: %v", n, err)
	}
	if n, err := op.CollectBlobTombstones(t.Context(), replacement, 10, deleter); n != 0 || err != nil {
		t.Fatalf("current empty pass=%d: %v", n, err)
	}
}
