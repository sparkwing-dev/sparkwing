package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// helper: enqueue a queue-policy waiter with cap=1, returns response.
func enqueue(t *testing.T, s *store.Store, key, run, node string, cost int) store.AcquireSlotResponse {
	t.Helper()
	return acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: run + "/" + node, RunID: run, NodeID: node,
		Capacity: 1, Cost: cost, Policy: store.OnLimitQueue,
	})
}

func liveHolders(t *testing.T, s *store.Store, key string) []store.ConcurrencyHolder {
	t.Helper()
	st, err := s.GetConcurrencyState(ctxT(t), key)
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	now := time.Now()
	var live []store.ConcurrencyHolder
	for _, h := range st.Holders {
		if !h.Superseded && h.LeaseExpiresAt.After(now) {
			live = append(live, h)
		}
	}
	return live
}

// One holder, three FIFO queued waiters. Cancel the MIDDLE waiter.
// On release the head promotes; after that head releases the tail
// promotes. The cancelled middle must never become a holder and the
// surviving order must stay r2 -> r4 (FIFO minus the hole).
func TestCancelQueued_MiddleWaiterRemoved_RestPromoteFIFO(t *testing.T) {
	s := newStoreT(t)
	key := "k"

	h := acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if h.Kind != store.AcquireGranted {
		t.Fatalf("holder: want granted, got %s", h.Kind)
	}
	if q := enqueue(t, s, key, "r2", "n", 1); q.Kind != store.AcquireQueued {
		t.Fatalf("r2 want queued got %s", q.Kind)
	}
	if q := enqueue(t, s, key, "r3", "n", 1); q.Kind != store.AcquireQueued {
		t.Fatalf("r3 want queued got %s", q.Kind)
	}
	if q := enqueue(t, s, key, "r4", "n", 1); q.Kind != store.AcquireQueued {
		t.Fatalf("r4 want queued got %s", q.Kind)
	}

	// Cancel the middle waiter (r3) while queued.
	removed, err := s.CancelWaiter(ctxT(t), key, "r3", "n")
	if err != nil {
		t.Fatalf("CancelWaiter: %v", err)
	}
	if !removed {
		t.Fatalf("CancelWaiter reported no row removed for queued r3")
	}

	// Release holder -> head (r2) promotes.
	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, "r1/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("ReleaseAndNotify r1: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r2" {
		t.Fatalf("after r1 release want promote r2, got %+v", promoted)
	}
	live := liveHolders(t, s, key)
	if len(live) != 1 || live[0].RunID != "r2" {
		t.Fatalf("want single live holder r2, got %+v", live)
	}

	// Release r2 -> r4 promotes (r3 was cancelled, must be skipped).
	_, _, promoted, err = s.ReleaseAndNotify(ctxT(t), key, "r2/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("ReleaseAndNotify r2: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r4" {
		t.Fatalf("after r2 release want promote r4, got %+v", promoted)
	}

	// r3 must never appear as a holder.
	st, err := s.GetConcurrencyState(ctxT(t), key)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	for _, hh := range st.Holders {
		if hh.RunID == "r3" {
			t.Fatalf("cancelled middle waiter r3 became a holder: %+v", hh)
		}
	}
	// Cost accounting: exactly r4 holds, used cost == 1, no over-admission.
	live = liveHolders(t, s, key)
	if len(live) != 1 || live[0].RunID != "r4" {
		t.Fatalf("want single live holder r4, got %+v", live)
	}
	if st.UsedCost != 1 {
		t.Fatalf("used cost want 1, got %d (over-admission/leak)", st.UsedCost)
	}
}

// Double-cancel must be idempotent: second cancel reports no row, no
// crash, no negative side effect on the rest of the queue.
func TestCancelQueued_DoubleCancel_Idempotent(t *testing.T) {
	s := newStoreT(t)
	key := "k"
	acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r1/n", RunID: "r1", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	enqueue(t, s, key, "r2", "n", 1)
	enqueue(t, s, key, "r3", "n", 1)

	first, err := s.CancelWaiter(ctxT(t), key, "r2", "n")
	if err != nil || !first {
		t.Fatalf("first cancel: removed=%v err=%v", first, err)
	}
	second, err := s.CancelWaiter(ctxT(t), key, "r2", "n")
	if err != nil {
		t.Fatalf("second cancel err: %v", err)
	}
	if second {
		t.Fatalf("double-cancel falsely reported a removed row; idempotency broken")
	}

	// r3 still promotes on release.
	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, "r1/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("after double-cancel of r2, want r3 promoted, got %+v", promoted)
	}
}

// Cancel the HEAD waiter just as a slot frees: the next-in-line must
// promote, and the cancelled head must never hold.
func TestCancelQueued_HeadCancelledAsSlotFrees(t *testing.T) {
	s := newStoreT(t)
	key := "k"
	acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r1/n", RunID: "r1", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	enqueue(t, s, key, "r2", "n", 1) // head
	enqueue(t, s, key, "r3", "n", 1) // next

	// Cancel head, then release: r3 must take the slot, not r2.
	if removed, err := s.CancelWaiter(ctxT(t), key, "r2", "n"); err != nil || !removed {
		t.Fatalf("cancel head: removed=%v err=%v", removed, err)
	}
	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, "r1/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("want r3 promoted after head r2 cancelled, got %+v", promoted)
	}
	for _, h := range liveHolders(t, s, key) {
		if h.RunID == "r2" {
			t.Fatalf("cancelled head r2 became holder: %+v", h)
		}
	}
}

// Cancel "mid-promotion": the waiter has ALREADY been promoted to a
// holder (the release/promote tx committed) and only THEN does the
// cancel land. CancelWaiter deletes from the waiters table by
// (key,run,node); a promoted node has no waiter row, so the cancel
// must be a no-op and must NOT delete the live holder. Verifies the
// fix doesn't strand or double-free a real slot.
func TestCancelQueued_AfterPromotion_DoesNotDropHolder(t *testing.T) {
	s := newStoreT(t)
	key := "k"
	acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r1/n", RunID: "r1", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	enqueue(t, s, key, "r2", "n", 1)

	// Release -> r2 promoted to holder.
	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, "r1/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r2" {
		t.Fatalf("want r2 promoted, got %+v", promoted)
	}

	// Late cancel lands AFTER promotion (e.g. ctx cancel raced the poll
	// that observed Promoted). Must not remove the holder.
	removed, err := s.CancelWaiter(ctxT(t), key, "r2", "n")
	if err != nil {
		t.Fatalf("late cancel err: %v", err)
	}
	if removed {
		t.Fatalf("late cancel removed a row though r2 was already a holder, not a waiter")
	}
	live := liveHolders(t, s, key)
	if len(live) != 1 || live[0].RunID != "r2" {
		t.Fatalf("promoted r2 holder dropped by late cancel: %+v", live)
	}
}

// Cost-weighted: capacity 3, holder cost 2, two queued cost-2 waiters.
// Cancel the head cost-2 waiter; on release the remaining cost-2 waiter
// must promote (2 fits in 3) and used cost must be exactly 2 -- no
// double-admission of the freed budget.
func TestCancelQueued_CostAccountingAfterCancel(t *testing.T) {
	s := newStoreT(t)
	key := "k"
	acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 3, Cost: 2, Policy: store.OnLimitQueue,
	})
	// Both queued: 2+2 > 3.
	if q := acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r2/n", RunID: "r2", NodeID: "n", Capacity: 3, Cost: 2, Policy: store.OnLimitQueue,
	}); q.Kind != store.AcquireQueued {
		t.Fatalf("r2 want queued got %s", q.Kind)
	}
	if q := acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "r3/n", RunID: "r3", NodeID: "n", Capacity: 3, Cost: 2, Policy: store.OnLimitQueue,
	}); q.Kind != store.AcquireQueued {
		t.Fatalf("r3 want queued got %s", q.Kind)
	}

	if removed, err := s.CancelWaiter(ctxT(t), key, "r2", "n"); err != nil || !removed {
		t.Fatalf("cancel r2: removed=%v err=%v", removed, err)
	}

	_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, "r1/n", "success", "", "", 0, time.Minute)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("want r3 promoted after r2 cancel, got %+v", promoted)
	}
	st, err := s.GetConcurrencyState(ctxT(t), key)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.UsedCost != 2 {
		t.Fatalf("used cost want 2 (only r3 holds), got %d", st.UsedCost)
	}
	live := liveHolders(t, s, key)
	if len(live) != 1 || live[0].RunID != "r3" {
		t.Fatalf("want single holder r3, got %+v", live)
	}
}
