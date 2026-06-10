package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A waiter promoted into a holder_id that still owns a superseded row (a
// CancelOthers eviction not yet reaped) must reclaim it via ON CONFLICT,
// not abort the promotion transaction on the UNIQUE constraint and
// strand the queue. The admission grant path got this clause; the
// promotion path needs the same.
func TestConcurrency_PromoteOntoSupersededHolderDoesNotCrash(t *testing.T) {
	s := newStoreT(t)
	// A holds the only slot.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	// B supersedes A under CancelOthers; A's holder row lingers
	// (superseded=1, contributes no active cost).
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	})
	// D takes the slot and holds it, filling the budget.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rD/n", RunID: "rD", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("D: want Granted, got %s", r.Kind)
	}
	// A re-arrives under Queue; the slot is taken by D, so A parks as a
	// waiter while its superseded holder row is still present.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("A re-arrival: want Queued (slot held by D, superseded row lingering), got %s", r.Kind)
	}
	// Releasing D promotes the FIFO-head waiter, which is B (it parked
	// when it superseded A).
	releaseAndPromoteT(t, s, "k", "rD/n")
	// Releasing B promotes A: the promotion insert reuses A's holder_id,
	// which still owns the lingering superseded row. Before the fix this
	// aborted the transaction on the UNIQUE constraint.
	promoted := releaseAndPromoteT(t, s, "k", "rB/n")
	var aPromoted bool
	for _, w := range promoted {
		if w.RunID == "rA" {
			aPromoted = true
		}
	}
	if !aPromoted {
		t.Fatalf("A was not promoted after D released; promoted=%+v", promoted)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders after promotion = %d, want 1", got)
	}
}
