package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// Release+promote is atomic — covers the "controller crashed between
// release tx and promote tx" window from Q4 of the RUN-015 self-
// testing pass. Even if the test stops the run between store calls,
// the DB state is consistent: either the release is committed and
// the next waiter is already promoted, or nothing happened.
func TestConcurrency_ReleaseAndNotifyIsAtomic(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	// Leader + 2 waiters.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w2", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	released, followers, promoted, err := s.ReleaseAndNotify(ctx, "k", "leader", "success", "", "", 0, 0)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released {
		t.Fatal("expected released=true")
	}
	if len(followers) != 0 {
		t.Fatalf("expected 0 coalesce followers, got %d", len(followers))
	}
	if len(promoted) != 1 || promoted[0].HolderID != "w1" {
		t.Fatalf("expected w1 promoted, got %+v", promoted)
	}

	// State: holder=w1, waiter=w2.
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "r2" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

// ReconcileConcurrencyKeys finds keys with queued waiters but no
// live holders and promotes. This is the recovery path for the
// "released then crashed before promote" window.
func TestConcurrency_ReconcileRecoversOrphanedQueue(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	// Simulate the crashed-mid-release state: acquire + manually
	// delete the holder row (bypassing ReleaseAndNotify so no promote
	// fires). This is what the DB would look like if the controller
	// died between release.commit and promote.commit.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	// Manually drop the holder (simulating an ACID release without promote).
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	// Pre-reconcile: state has 0 holders, 1 waiter. Stuck.
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 0 || len(state.Waiters) != 1 {
		t.Fatalf("setup: expected 0 holders + 1 waiter, got %+v", state)
	}

	promoted, err := s.ReconcileConcurrencyKeys(ctx, store.DefaultConcurrencyLease)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if promoted != 1 {
		t.Fatalf("expected 1 waiter promoted, got %d", promoted)
	}

	// Post-reconcile: w1 is the holder.
	state, _ = s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("post-reconcile holders: %+v", state.Holders)
	}
}

// Orphan coalesce followers get reaped when their leader is gone.
func TestConcurrency_WaiterReaperDropsOrphanFollowers(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	// Leader + coalesce follower.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	resp := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "follower", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCoalesce,
	})
	if resp.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", resp.Kind)
	}

	// Simulate leader release WITHOUT ResolveCoalesceFollowers running
	// (controller crash between release tx and resolve tx). Drop the
	// holder row by hand.
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	// Waiter reaper should detect the orphan follower and drop it.
	dropped, err := s.ReapStaleConcurrencyWaiters(ctx, time.Hour)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(dropped) != 1 || dropped[0].RunID != "r1" {
		t.Fatalf("expected r1 dropped, got %+v", dropped)
	}
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Waiters) != 0 {
		t.Fatalf("waiter should have been reaped, got %+v", state.Waiters)
	}
}

// Waiters past maxAge get reaped regardless of leader state.
func TestConcurrency_WaiterReaperDropsOldWaiters(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	// Fast-forward the waiter's arrived_at into the past by rewriting
	// the column directly. Simulates a caller that crashed an hour ago
	// and never called cancel.
	oneHourAgo := time.Now().Add(-time.Hour).UnixNano()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ?`,
		oneHourAgo, "k", "r1"); err != nil {
		t.Fatalf("rewrite arrived_at: %v", err)
	}

	dropped, err := s.ReapStaleConcurrencyWaiters(ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected 1 reaped, got %d", len(dropped))
	}

	// A more recent waiter stays.
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w2", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	dropped, _ = s.ReapStaleConcurrencyWaiters(ctx, 10*time.Minute)
	if len(dropped) != 0 {
		t.Fatalf("fresh waiter should not be reaped: %+v", dropped)
	}
}

// Regression: when maxAge is 0 the reaper is a no-op (disables the
// age-based pass). Protects against accidental "reap everything"
// calls if the config passes a zero.
func TestConcurrency_WaiterReaperZeroAgeIsNoop(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	dropped, err := s.ReapStaleConcurrencyWaiters(ctx, 0)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("zero-age reap should drop nothing, got %d", len(dropped))
	}
}

var _ = context.Background // silence unused-import check when no direct use
