package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A waiter promoted into a holder_id that still owns a superseded row (a
// CancelOthers eviction not yet reaped) must reclaim it via ON CONFLICT,
// not abort the promotion transaction on the UNIQUE constraint and
// strand the queue. The admission grant path got this clause; the
// promotion path needs the same.
func TestConcurrency_PromoteOntoSupersededHolderDoesNotCrash(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	}); r.Kind != store.AcquireCancellingOthers {
		t.Fatalf("B: want CancellingOthers, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("A re-arrival: want Queued (B holds, superseded row lingering), got %s", r.Kind)
	}
	promoted := releaseAndPromoteT(t, s, "k", "rB/n")
	var aPromoted bool
	for _, w := range promoted {
		if w.RunID == "rA" {
			aPromoted = true
		}
	}
	if !aPromoted {
		t.Fatalf("A was not promoted after B released; promoted=%+v", promoted)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders after promotion = %d, want 1", got)
	}
}

// A waiter promoted into a holder_id that still owns a lease-expired
// (but not yet reaped) row must reclaim it the same way the admission
// grant path does, not abort the release transaction on the UNIQUE
// constraint and strand the queue.
func TestConcurrency_PromoteOntoExpiredHolderReclaimsRow(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Nanosecond,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("A re-arrival: want Queued (B holds, expired row lingering), got %s", r.Kind)
	}
	promoted := releaseAndPromoteT(t, s, "k", "rB/n")
	var aPromoted bool
	for _, w := range promoted {
		if w.RunID == "rA" {
			aPromoted = true
		}
	}
	if !aPromoted {
		t.Fatalf("A was not promoted after B released; promoted=%+v", promoted)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders after promotion = %d, want 1", got)
	}
}
