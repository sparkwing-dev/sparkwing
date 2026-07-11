package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Release+promote is atomic -- covers the "controller crashed between
// release tx and promote tx" window. Even if the test stops the run
// between store calls,
// the DB state is consistent: either the release is committed and
// the next waiter is already promoted, or nothing happened.
func TestConcurrency_ReleaseAndNotifyIsAtomic(t *testing.T) {
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

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	createLiveRunT(t, s, "r1")
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 0 || len(state.Waiters) != 1 {
		t.Fatalf("setup: expected 0 holders + 1 waiter, got %+v", state)
	}

	promoted, err := store.Maintenance.ReconcileConcurrencyKeys(s, ctx, store.DefaultConcurrencyLease)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if promoted != 1 {
		t.Fatalf("expected 1 waiter promoted, got %d", promoted)
	}

	state, _ = s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("post-reconcile holders: %+v", state.Holders)
	}
}

func TestConcurrency_ResolveWaiterPromotesOrphanedQueue(t *testing.T) {
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
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := s.ResolveWaiter(ctx, "k", "r1", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "w1" {
		t.Fatalf("resolution = %+v", resolution)
	}
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

func TestConcurrency_ResolveWaiterSkipsAbandonedFIFOHead(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "abandoned", RunID: "abandoned", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "live", RunID: "live", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := s.ResolveWaiter(ctx, "k", "live", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "live" {
		t.Fatalf("resolution = %+v, want live promoted", resolution)
	}
	if holderExists(t, s, "k", "abandoned") {
		t.Fatalf("abandoned FIFO head was promoted into a holder")
	}
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Waiters) != 0 {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

func TestConcurrency_PromoteUsesRunningRunCreatedBeforeHeartbeatLoop(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err := s.CreateRun(ctx, store.Run{
		ID:        "queued",
		Pipeline:  "queued-pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if r := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "queued", RunID: "queued", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("queued run: want Queued, got %s", r.Kind)
	}

	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}
	promoted, err := s.PromoteNextWaiters(ctx, "k", store.DefaultConcurrencyLease)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted) != 1 || promoted[0].HolderID != "queued" {
		t.Fatalf("promoted = %+v, want queued", promoted)
	}
}

func TestConcurrency_ResolveWaiterPromotesOrphanedPlanQueue(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader/-", RunID: "leader", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "waiter/-", RunID: "waiter", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader/-"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := s.ResolveWaiter(ctx, "k", "waiter", "", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "waiter/-" {
		t.Fatalf("resolution = %+v", resolution)
	}
	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter" || state.Holders[0].NodeID != "" {
		t.Fatalf("holders = %+v", state.Holders)
	}
}

func TestConcurrency_ResolveWaiterSeesAlreadyPromotedHolder(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder", RunID: "waiter", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	resolution, err := s.ResolveWaiter(ctx, "k", "waiter", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "holder" {
		t.Fatalf("resolution = %+v", resolution)
	}
}

// Orphan coalesce followers get reaped when their leader is gone.
func TestConcurrency_WaiterReaperDropsOrphanFollowers(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

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

	if _, err := s.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	dropped, err := store.Maintenance.ReapStaleConcurrencyWaiters(s, ctx, time.Hour)
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
	acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	oneHourAgo := time.Now().Add(-time.Hour).UnixNano()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ?`,
		oneHourAgo, "k", "r1"); err != nil {
		t.Fatalf("rewrite arrived_at: %v", err)
	}

	dropped, err := store.Maintenance.ReapStaleConcurrencyWaiters(s, ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected 1 reaped, got %d", len(dropped))
	}

	acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w2", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	dropped, _ = store.Maintenance.ReapStaleConcurrencyWaiters(s, ctx, 10*time.Minute)
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
	dropped, err := store.Maintenance.ReapStaleConcurrencyWaiters(s, ctx, 0)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("zero-age reap should drop nothing, got %d", len(dropped))
	}
}

var _ = context.Background // silence unused-import check when no direct use
