package store_test

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func TestConcurrency_ReacquireExpiredHolderDoesNotRevive(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: 40 * time.Millisecond,
	})
	started := time.Now()
	if _, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), "k", "rA/n",
	); err != nil {
		t.Fatalf("expire holder: %v", err)
	}
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
	if elapsed := time.Since(started); elapsed >= 60*time.Millisecond {
		t.Fatalf("expired-holder reassignment took %v, want less than 60ms", elapsed)
	}
}

func TestConcurrency_BudgetOverflowDoesNotOverAdmit(t *testing.T) {
	s := storetest.Open(t)
	big := math.MaxInt/3 + 1
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

func TestConcurrency_ZeroDeclaredCapacityHolderConstrainsFloor(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	})
	if _, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE concurrency_holders SET declared_capacity = 0 WHERE key = ? AND holder_id = ?`),
		"k", "rA/n",
	); err != nil {
		t.Fatalf("inject zero-cap holder: %v", err)
	}
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

func TestConcurrency_CancelWaiterReclaimsPromotedHolder(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued, got %s", r.Kind)
	}
	releaseAndPromoteT(t, s, "k", "rA/n")
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("after A release: active holders = %d, want 1 (B promoted)", got)
	}
	matched, err := s.CancelWaiter(ctxT(t), "k", "rB", "n")
	if err != nil {
		t.Fatalf("CancelWaiter: %v", err)
	}
	if !matched {
		t.Fatalf("CancelWaiter matched nothing; the promoted holder was left orphaned")
	}
	if got := activeHolders(t, s, "k"); got != 0 {
		t.Fatalf("active holders after cancel = %d, want 0 (orphan reclaimed)", got)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rC/n", RunID: "rC", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("C: want Granted (slot freed), got %s", r.Kind)
	}
}

func TestConcurrency_ResolveWaiterBypassReadSkipsCache(t *testing.T) {
	s := storetest.Open(t)
	now := time.Now()
	if _, err := s.DB().Exec(storetest.Rebind(s,
		`INSERT INTO concurrency_cache
		   (key, cache_key_hash, output_ref, origin_run_id, origin_node_id, created_at, expires_at, last_hit_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		"memo:k", "h1", "out-ref", "r0", "n0", now.UnixNano(), now.Add(time.Hour).UnixNano(), now.UnixNano(),
	); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if res, err := s.ResolveWaiter(ctxT(t), "memo:k", "rF", "n", "h1", "", "", false); err != nil {
		t.Fatalf("resolve (no bypass): %v", err)
	} else if res.Status != store.WaiterCached {
		t.Fatalf("no-bypass follower status = %q, want Cached", res.Status)
	}
	if res, err := s.ResolveWaiter(ctxT(t), "memo:k", "rF2", "n", "h1", "", "", true); err != nil {
		t.Fatalf("resolve (bypass): %v", err)
	} else if res.Status == store.WaiterCached {
		t.Fatalf("--no-cache follower got Cached; bypass-read ignored on the resolve path")
	}
}

func TestConcurrency_FreshArrivalDoesNotBargeQueuedWaiter(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue, Lease: 40 * time.Millisecond,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rW/n", RunID: "rW", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("W: want Queued, got %s", r.Kind)
	}
	started := time.Now()
	updated, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), "k", "rA/n",
	)
	if err != nil {
		t.Fatalf("expire holder: %v", err)
	}
	if rows, err := updated.RowsAffected(); err != nil {
		t.Fatalf("count expired holders: %v", err)
	} else if rows != 1 {
		t.Fatalf("expired holder rows = %d, want 1", rows)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rX/n", RunID: "rX", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("X: want Queued (FIFO; must not barge W), got %s", r.Kind)
	}
	if got := activeHolders(t, s, "k"); got != 0 {
		t.Fatalf("active holders after expiry = %d, want 0", got)
	}
	state, err := s.GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("state after fresh arrival: %v", err)
	}
	if len(state.Waiters) != 2 || state.Waiters[0].RunID != "rW" || state.Waiters[1].RunID != "rX" {
		t.Fatalf("waiter order = %+v, want rW then rX", state.Waiters)
	}
	if elapsed := time.Since(started); elapsed >= 60*time.Millisecond {
		t.Fatalf("expired-holder FIFO check took %v, want less than 60ms", elapsed)
	}
}

func TestConcurrency_ResolveWaiterRejectsFailedLeaderOutcome(t *testing.T) {
	s := storetest.Open(t)
	seedRunAndNode(t, s, "rLeader", "n")
	if err := s.FinishNodeWithReason(ctxT(t), "rLeader", "n",
		"failed", "boom", nil, store.FailureOOMKilled, nil); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}
	res, err := s.ResolveWaiter(ctxT(t), "k", "rF", "n", "", "rLeader", "n", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if res.Status != store.WaiterCancelled {
		t.Fatalf("status = %q, want Cancelled", res.Status)
	}
}

func TestConcurrency_BypassReadNodeQueuesInsteadOfCoalescing(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "memo:k", HolderID: "rL/n", RunID: "rL", NodeID: "n",
		Capacity: 1, Cost: 1, CacheKeyHash: "h1", Policy: store.OnLimitCoalesce,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "memo:k", HolderID: "rF/n", RunID: "rF", NodeID: "n",
		Capacity: 1, Cost: 1, CacheKeyHash: "h1", Policy: store.OnLimitCoalesce,
	}); r.Kind != store.AcquireCoalesced {
		t.Fatalf("normal follower: want Coalesced, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "memo:k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Cost: 1, CacheKeyHash: "h1", Policy: store.OnLimitCoalesce, BypassRead: true,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("--no-cache follower: want Queued (run fresh), got %s", r.Kind)
	}
}

func TestConcurrency_CancelOthersGrantsAndReservesBudget(t *testing.T) {
	s := storetest.Open(t)
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
		Key: "k", HolderID: "rC/n", RunID: "rC", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("C: want Queued (B holds the slot), got %s", r.Kind)
	}
	r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rD/n", RunID: "rD", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	})
	if r.Kind != store.AcquireCancellingOthers {
		t.Fatalf("D: want CancellingOthers, got %s", r.Kind)
	}
	if len(r.SupersededIDs) != 1 || r.SupersededIDs[0] != "rB/n" {
		t.Fatalf("D: SupersededIDs = %v, want [rB/n] (must supersede the canceller)", r.SupersededIDs)
	}
}
