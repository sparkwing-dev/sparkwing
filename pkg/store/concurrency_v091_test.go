package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// activeHolders counts non-superseded, unexpired holders for a key.
func activeHolders(t *testing.T, s *store.Store, key string) int {
	t.Helper()
	st, err := s.GetConcurrencyState(ctxT(t), key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0
		}
		t.Fatalf("GetConcurrencyState(%s): %v", key, err)
	}
	now := time.Now()
	n := 0
	for _, h := range st.Holders {
		if !h.Superseded && h.LeaseExpiresAt.After(now) {
			n++
		}
	}
	return n
}

// Defect 1: a heartbeat landing after the lease expired must NOT revive
// the holder -- admission may have already reassigned that budget, so
// reviving would put two live holders on a capacity-1 key.
func TestConcurrency_HeartbeatOnExpiredLeaseDoesNotRevive(t *testing.T) {
	s := newStoreT(t)
	// Holder A takes the only slot with a short lease.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: 40 * time.Millisecond,
	})
	time.Sleep(80 * time.Millisecond) // A's lease lapses

	// Admission reassigns the freed budget to B (A is expired, uncounted).
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted (A expired), got %s", r.Kind)
	}

	// A's delayed heartbeat must fail, not revive A.
	_, _, err := s.HeartbeatConcurrencySlot(ctxT(t), "k", "rA/n", time.Minute)
	if !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("heartbeat on expired lease err = %v, want ErrLockHeld", err)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders on capacity-1 key = %d, want 1 (no over-admission)", got)
	}
}

// Defect 2: re-acquiring a holder_id whose row is superseded-but-live
// must take the slot cleanly via ON CONFLICT, not crash on the UNIQUE
// constraint.
func TestConcurrency_ReacquireSupersededHolderDoesNotCrash(t *testing.T) {
	s := newStoreT(t)
	// A holds the only slot.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	// B arrives under CancelOthers and supersedes A (A's row stays, live
	// lease, superseded=1).
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	}); r.Kind != store.AcquireCancellingOthers {
		t.Fatalf("B: want CancellingOthers, got %s", r.Kind)
	}
	// A's holder_id re-acquires (deterministic runID/nodeID on a
	// crash/redeliver). The superseded row is still present; the grant
	// must reclaim it rather than collide.
	resp, err := s.AcquireConcurrencySlot(ctxT(t), store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("re-acquire of superseded holder crashed: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("re-acquire kind = %s, want Granted", resp.Kind)
	}
}

// Defect 6: a parked low-capacity waiter must not drag effective
// capacity below the already-admitted holders (used <= effective).
func TestConcurrency_ParkedWaiterDoesNotInvertEffectiveCapacity(t *testing.T) {
	s := newStoreT(t)
	// A cap-4 cost-4 holder fills the budget.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	})
	// A cap-3 cost-1 waiter queues (budget full).
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 3, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued, got %s", r.Kind)
	}
	st, err := s.GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	// Before the fix the parked cap-3 waiter forced eff=3 against used=4.
	if st.UsedCost > st.EffectiveCapacity {
		t.Fatalf("used=%d > effective=%d: a parked waiter dragged effective capacity below admitted holders",
			st.UsedCost, st.EffectiveCapacity)
	}
}

// Defect 6 (cont.): a later parked low-capacity waiter must not block a
// FIFO-head waiter that fits under its own declared capacity.
func TestConcurrency_ParkedWaiterDoesNotStallFIFOHeadPromotion(t *testing.T) {
	s := newStoreT(t)
	// Holder fills a cap-4 budget.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "h/n", RunID: "h", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	})
	// FIFO head: cap-4 cost-2, queues.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "head/n", RunID: "head", NodeID: "n",
		Capacity: 4, Cost: 2, Policy: store.OnLimitQueue,
	})
	// Behind it: cap-1 cost-1, queues. Its low cap must not pin the
	// effective capacity and block the head.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "tail/n", RunID: "tail", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	})
	// Release the holder; the head (cost 2) fits the freed cap-4 budget.
	promoted := releaseAndPromoteT(t, s, "k", "h/n")
	var headPromoted bool
	for _, w := range promoted {
		if w.RunID == "head" {
			headPromoted = true
		}
	}
	if !headPromoted {
		t.Fatalf("FIFO-head cost-2 waiter was not promoted into the freed cap-4 budget; promoted=%+v", promoted)
	}
}

// Defect 7: an arrival whose cost exceeds capacity can never be admitted
// and must be rejected at admission, not queued to strand forever.
func TestConcurrency_CostOverCapacityRejectedNotStranded(t *testing.T) {
	s := newStoreT(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r/n", RunID: "r", NodeID: "n",
		Capacity: 4, Cost: 5, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireFailed {
		t.Fatalf("cost>capacity under Queue: kind = %s, want Failed (not stranded in the queue)", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k2", HolderID: "r/n", RunID: "r", NodeID: "n",
		Capacity: 4, Cost: 5, Policy: store.OnLimitSkip,
	}); r.Kind != store.AcquireSkipped {
		t.Fatalf("cost>capacity under Skip: kind = %s, want Skipped", r.Kind)
	}
}
