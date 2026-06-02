package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConcurrency_QueuedResponseCarriesPositionAndHolders(t *testing.T) {
	s := newStoreT(t)

	h := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if h.Kind != store.AcquireGranted {
		t.Fatalf("holder: want Granted got %s", h.Kind)
	}

	q1 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if q1.Kind != store.AcquireQueued {
		t.Fatalf("q1: want Queued got %s", q1.Kind)
	}
	if q1.Position != 0 {
		t.Fatalf("q1 position = %d, want 0 (next in line)", q1.Position)
	}
	if q1.QueueLength != 1 {
		t.Fatalf("q1 queue length = %d, want 1", q1.QueueLength)
	}
	if len(q1.Holders) != 1 || q1.Holders[0].RunID != "r1" {
		t.Fatalf("q1 holders = %+v, want one holder r1", q1.Holders)
	}

	q2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r3/n1", RunID: "r3", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if q2.Kind != store.AcquireQueued {
		t.Fatalf("q2: want Queued got %s", q2.Kind)
	}
	if q2.Position != 1 {
		t.Fatalf("q2 position = %d, want 1 (one ahead)", q2.Position)
	}
	if q2.QueueLength != 2 {
		t.Fatalf("q2 queue length = %d, want 2", q2.QueueLength)
	}
}

func TestConcurrency_StateDerivesWaiterPositions(t *testing.T) {
	s := newStoreT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r3/n1", RunID: "r3", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	st, err := s.GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if len(st.Holders) != 1 {
		t.Fatalf("holders = %d, want 1", len(st.Holders))
	}
	if len(st.Waiters) != 2 {
		t.Fatalf("waiters = %d, want 2", len(st.Waiters))
	}
	if st.Waiters[0].Position != 0 || st.Waiters[1].Position != 1 {
		t.Fatalf("waiter positions = [%d, %d], want [0, 1]",
			st.Waiters[0].Position, st.Waiters[1].Position)
	}
}
