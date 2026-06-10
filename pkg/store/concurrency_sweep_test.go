package store_test

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// D3: the idempotent re-acquire path is the twin of the heartbeat-revive
// guard. Re-acquiring an expired holder_id must not revive it onto a
// slot whose budget was already reassigned.
func TestConcurrency_ReacquireExpiredHolderDoesNotRevive(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: 40 * time.Millisecond,
	})
	time.Sleep(80 * time.Millisecond) // A's lease lapses
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted (A expired), got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind == store.AcquireGranted {
		t.Fatalf("re-acquire of expired holder was Granted; over-admission")
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders on cap-1 key = %d, want 1 (no revive)", got)
	}
}

// D2: budget arithmetic must not overflow into a false "fits". Several
// holders with huge declared costs whose sum wraps past MaxInt must not
// all be admitted.
func TestConcurrency_BudgetOverflowDoesNotOverAdmit(t *testing.T) {
	s := newStoreT(t)
	big := math.MaxInt/3 + 1 // three of these overflow the running sum
	holders := []string{"r1/n", "r2/n", "r3/n"}
	granted := 0
	for _, h := range holders {
		r := acquireT(t, s, store.AcquireSlotRequest{
			Key: "k", HolderID: h, RunID: h[:2], NodeID: "n",
			Capacity: math.MaxInt, Cost: big, Policy: store.OnLimitQueue,
		})
		if r.Kind == store.AcquireGranted {
			granted++
		}
	}
	if granted > 2 {
		t.Fatalf("granted %d holders; cost sum overflowed into over-admission", granted)
	}
	st, err := s.GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.UsedCost < 0 {
		t.Fatalf("UsedCost = %d (negative => overflow / over-admission)", st.UsedCost)
	}
}

// D5: a holder carrying declared_capacity<=0 (a v3-migration backfill or
// a promoted legacy waiter) must still constrain the effective-capacity
// floor, not vanish from it and let a higher-declaring arrival
// over-admit.
func TestConcurrency_ZeroDeclaredCapacityHolderConstrainsFloor(t *testing.T) {
	s := newStoreT(t)
	// A holds the cap-1 budget; the key's entry capacity is 1.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	})
	// Simulate a v3-migration survivor: a live holder whose
	// declared_capacity was backfilled to 0 across the upgrade.
	if _, err := s.DB().Exec(
		`UPDATE concurrency_holders SET declared_capacity = 0 WHERE key = ? AND holder_id = ?`,
		"k", "rA/n",
	); err != nil {
		t.Fatalf("inject zero-cap holder: %v", err)
	}
	// C declares a big capacity. If the zero-cap holder is invisible to
	// the floor, C sees an inflated effective capacity and is granted on a
	// cap-1 key -> over-admission.
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rC/n", RunID: "rC", NodeID: "n",
		Capacity: 100, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind == store.AcquireGranted {
		t.Fatalf("C granted; the zero-declared-capacity holder was invisible to the floor (over-admission)")
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders on cap-1 key = %d, want 1", got)
	}
}
