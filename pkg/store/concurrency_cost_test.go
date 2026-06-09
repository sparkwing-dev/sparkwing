package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Cost-weighted admission: capacity is a budget summed over live
// holders' costs, not a slot count. Capacity 8 with cost-4 members
// admits two; the third waits until one drains.
func TestConcurrency_CostWeightedAdmission(t *testing.T) {
	s := newStoreT(t)
	mk := func(run string) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: "db", HolderID: run + "/n", RunID: run, NodeID: "n",
			Capacity: 8, Cost: 4, Policy: store.OnLimitQueue,
		}
	}
	if r := acquireT(t, s, mk("r1")); r.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, mk("r2")); r.Kind != store.AcquireGranted {
		t.Fatalf("r2: want Granted (4+4<=8) got %s", r.Kind)
	}
	if r := acquireT(t, s, mk("r3")); r.Kind != store.AcquireQueued {
		t.Fatalf("r3: want Queued (8+4>8) got %s", r.Kind)
	}

	// Drain r1; the queued cost-4 member now fits and is promoted.
	promoted := releaseAndPromoteT(t, s, "db", "r1/n")
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("expected r3 promoted, got %+v", promoted)
	}
}

// releaseAndPromoteT releases a holder and returns the waiters promoted
// in the same transaction.
func releaseAndPromoteT(t *testing.T, s *store.Store, key, holderID string) []store.ConcurrencyWaiter {
	t.Helper()
	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, holderID, "success", "", "", 0, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify(%s,%s): %v", key, holderID, err)
	}
	return promoted
}

// A waiter whose cost does not fit the freed budget is not promoted,
// and a cheaper waiter behind it does not jump ahead (FIFO, one
// dimension).
func TestConcurrency_CostHeavyWaiterHoldsFIFO(t *testing.T) {
	s := newStoreT(t)
	// Capacity 4. r1 cost 4 holds the whole budget.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r.Kind)
	}
	// r2 cost 4 queues first, r3 cost 1 queues behind it.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r2/n", RunID: "r2", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r3/n", RunID: "r3", NodeID: "n",
		Capacity: 4, Cost: 1, Policy: store.OnLimitQueue,
	})

	// Free 1 unit is not enough for the head (cost 4); nobody promotes,
	// and the cheap r3 must not skip ahead.
	promoted := releaseAndPromoteT(t, s, "db", "r1/n")
	if len(promoted) != 1 || promoted[0].RunID != "r2" {
		t.Fatalf("expected only r2 promoted once full budget freed, got %+v", promoted)
	}
}

// Most-restrictive-wins: when live participants declare different
// capacities, the effective capacity is the minimum. A higher
// declaration cannot overcommit while a lower one is live; it takes
// effect only after the lower drains.
func TestConcurrency_MostRestrictiveCapacityWins(t *testing.T) {
	s := newStoreT(t)
	// A declares capacity 5, cost 1 -> granted.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 5, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("A: want Granted got %s", r.Kind)
	}
	// B declares capacity 2, cost 1 -> granted (sum 2 <= min(5,2)=2).
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 2, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted got %s", r.Kind)
	}
	// C declares capacity 5, cost 1 -> effective is min(5,2,5)=2, sum
	// would be 3 > 2, so it queues despite its own higher declaration.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rC/n", RunID: "rC", NodeID: "n",
		Capacity: 5, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("C: want Queued under effective cap 2, got %s", r.Kind)
	}

	// Drain the restrictive participant (B). Now only 5-declarers remain
	// live, so effective rises to 5 and C is promoted.
	promoted := releaseAndPromoteT(t, s, "db", "rB/n")
	if len(promoted) != 1 || promoted[0].RunID != "rC" {
		t.Fatalf("expected rC promoted once the cap-2 participant drained, got %+v", promoted)
	}
}
