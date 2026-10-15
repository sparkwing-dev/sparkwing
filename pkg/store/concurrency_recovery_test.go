package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestConcurrency_ReleaseAndNotifyIsAtomic(t *testing.T) {
	s := storetest.Open(t)
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

	released, followers, promoted, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "k", "leader", "success", "", "", 0, 0)
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

	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "r2" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

func TestConcurrency_ReconcileRecoversOrphanedQueue(t *testing.T) {
	s := storetest.Open(t)
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
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 0 || len(state.Waiters) != 1 {
		t.Fatalf("setup: expected 0 holders + 1 waiter, got %+v", state)
	}

	result, err := teststore.MaintainConcurrency(ctx, s, store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Promoted != 1 {
		t.Fatalf("expected 1 waiter promoted, got %d", result.Promoted)
	}

	state, _ = tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("post-reconcile holders: %+v", state.Holders)
	}
}

func TestConcurrency_ResolveWaiterPromotesOrphanedQueue(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w1", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "k", "r1", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "w1" {
		t.Fatalf("resolution = %+v", resolution)
	}
	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "w1" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

func TestConcurrency_ResolveWaiterSkipsAbandonedFIFOHead(t *testing.T) {
	s := storetest.Open(t)
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
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "k", "live", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "live" {
		t.Fatalf("resolution = %+v, want live promoted", resolution)
	}
	if holderExists(t, s, "k", "abandoned") {
		t.Fatalf("abandoned FIFO head was promoted into a holder")
	}
	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Waiters) != 0 {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}

func TestConcurrency_PromoteUsesRunningRunCreatedBeforeHeartbeatLoop(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateRun(ctx, store.Run{
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

	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}
	promoted, err := tenantFor(t, s, store.DefaultTeam).PromoteNextWaiters(ctx, "k", store.DefaultConcurrencyLease)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted) != 1 || promoted[0].HolderID != "queued" {
		t.Fatalf("promoted = %+v, want queued", promoted)
	}
}

func TestConcurrency_PromotePreservesFreshRunningRunWithNullHeartbeat(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateRun(ctx, store.Run{
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

	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}
	promoted, err := tenantFor(t, s, store.DefaultTeam).PromoteNextWaiters(ctx, "k", store.DefaultConcurrencyLease)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(promoted) != 1 || promoted[0].HolderID != "queued" {
		t.Fatalf("promoted = %+v, want queued", promoted)
	}
}

func TestConcurrency_ResolveWaiterPromotesOrphanedPlanQueue(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader/-", RunID: "leader", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "waiter/-", RunID: "waiter", NodeID: "",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader/-"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "k", "waiter", "", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "waiter/-" {
		t.Fatalf("resolution = %+v", resolution)
	}
	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter" || state.Holders[0].NodeID != "" {
		t.Fatalf("holders = %+v", state.Holders)
	}
}

func TestConcurrency_ResolveWaiterSeesAlreadyPromotedHolder(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "holder", RunID: "waiter", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "k", "waiter", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "holder" {
		t.Fatalf("resolution = %+v", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesWhenLeaderRunEndsWithoutNodeVerdict(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-run",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-run",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}
	if err := teststore.SeedRunFinish(s, ctx, store.DefaultTeam, "leader", "cancelled", "executor cancelled before node verdict"); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-cancelled-run", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerWaitsForLiveLeaderWithoutNodeVerdict(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-live-leader",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-live-leader",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-live-leader", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterStillWaiting {
		t.Fatalf("resolution = %+v, want follower still waiting on live leader", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesAfterCancelledLeaderRelease(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-release",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-release",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}

	released, followers, _, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "cache-cancelled-release", "leader/node", "cancelled", "", "same-content", time.Hour, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseAndNotify released=false")
	}
	if len(followers) != 0 {
		t.Fatalf("cancelled leader must not resolve cached followers instead of resolving followers, got %+v", followers)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-cancelled-release", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesAfterFailedLeaderRelease(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-failed-release",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: "leader", NodeID: "node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-failed-release",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}
	if err := teststore.SeedNodeFinish(s, ctx, store.DefaultTeam, store.NodeRef{RunID: "leader", NodeID: "node"}, store.NodeResult{Outcome: store.NodeState("failed"), MaskedError: "command terminated"}); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	released, followers, _, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "cache-failed-release", "leader/node", "failed", "", "same-content", time.Hour, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseAndNotify released=false")
	}
	if len(followers) != 0 {
		t.Fatalf("failed leader must not resolve followers, got %+v", followers)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-failed-release", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesAfterSkippedLeaderRelease(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-skipped-release",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: "leader", NodeID: "node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-skipped-release",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}
	if err := teststore.SeedNodeFinish(s, ctx, store.DefaultTeam, store.NodeRef{RunID: "leader", NodeID: "node"}, store.NodeResult{Outcome: store.NodeState("skipped"), MaskedError: ""}); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	released, followers, _, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "cache-skipped-release", "leader/node", "skipped", "", "same-content", time.Hour, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseAndNotify released=false")
	}
	if len(followers) != 0 {
		t.Fatalf("skipped leader must not resolve followers, got %+v", followers)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-skipped-release", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerSurvivesMaintenanceAfterCancelledLeaderRelease(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-leader-maintenance",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-cancelled-leader-maintenance",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}

	released, followers, _, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "cache-cancelled-leader-maintenance", "leader/node", "cancelled", "", "same-content", time.Hour, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseAndNotify released=false")
	}
	if len(followers) != 0 {
		t.Fatalf("cancelled leader must not resolve cached followers, got %+v", followers)
	}
	result, err := teststore.MaintainConcurrency(ctx, s, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Hour})
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.StaleWaiters) != 0 {
		t.Fatalf("live follower should survive maintenance, dropped %+v", result.StaleWaiters)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-cancelled-leader-maintenance", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesAfterLeaderAgentLost(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-agent-lost-leader",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: "leader", NodeID: "node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-agent-lost-leader",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}
	if err := teststore.SeedNodeFinish(s, ctx, store.DefaultTeam, store.NodeRef{RunID: "leader", NodeID: "node"}, store.NodeResult{Outcome: store.NodeState("failed"), MaskedError: "runner heartbeat expired", Reason: store.FailureAgentLost, ExitCode: nil}); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"cache-agent-lost-leader", "leader/node"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-agent-lost-leader", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerPromotesAfterLeaderAgentLostRelease(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	leader := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-agent-lost-release",
		HolderID:     "leader/node",
		RunID:        "leader",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitQueue,
		CacheKeyHash: "same-content",
		Lease:        time.Hour,
	})
	if leader.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", leader.Kind)
	}
	if err := teststore.SeedNode(s, ctx, store.Node{RunID: "leader", NodeID: "node", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:          "cache-agent-lost-release",
		HolderID:     "follower/node",
		RunID:        "follower",
		NodeID:       "node",
		Capacity:     1,
		Policy:       store.OnLimitCoalesce,
		CacheKeyHash: "same-content",
	})
	if follower.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", follower.Kind)
	}
	if err := teststore.SeedNodeFinish(s, ctx, store.DefaultTeam, store.NodeRef{RunID: "leader", NodeID: "node"}, store.NodeResult{Outcome: store.NodeState("failed"), MaskedError: "runner heartbeat expired", Reason: store.FailureAgentLost, ExitCode: nil}); err != nil {
		t.Fatalf("FinishNodeWithReason: %v", err)
	}
	released, followers, _, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "cache-agent-lost-release", "leader/node", "failed", "", "same-content", time.Hour, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify: %v", err)
	}
	if !released {
		t.Fatalf("ReleaseAndNotify released=false")
	}
	if len(followers) != 0 {
		t.Fatalf("agent-lost leader must not resolve cached followers, got %+v", followers)
	}

	resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-agent-lost-release", "follower", "node", "same-content", "leader", "node", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if resolution.Status != store.WaiterPromoted || resolution.HolderID != "follower/node" {
		t.Fatalf("resolution = %+v, want follower promoted to execute", resolution)
	}
}

func TestConcurrency_CoalesceFollowerRejectsNonReusableCompletedOutcomes(t *testing.T) {
	for _, outcome := range []string{"satisfied", "skipped-concurrent"} {
		t.Run(outcome, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := ctxT(t)
			seedRunAndNode(t, s, "leader", "node")
			if err := teststore.SeedNodeFinish(s, ctx, store.DefaultTeam, store.NodeRef{RunID: "leader", NodeID: "node"}, store.NodeResult{Outcome: store.NodeState(outcome), MaskedError: ""}); err != nil {
				t.Fatalf("FinishNode: %v", err)
			}

			resolution, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctx, "cache-canonical-outcome", "follower", "node", "same-content", "leader", "node", false)
			if err != nil {
				t.Fatalf("ResolveWaiter: %v", err)
			}
			if resolution.Status != store.WaiterCancelled {
				t.Fatalf("resolution = %+v, want cancelled", resolution)
			}
		})
	}
}

func TestConcurrency_WaiterReaperDropsOrphanFollowers(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader", RunID: "r0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	resp := acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "follower", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCoalesce,
	})
	if resp.Kind != store.AcquireCoalesced {
		t.Fatalf("follower: want Coalesced got %s", resp.Kind)
	}

	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`DELETE FROM concurrency_holders WHERE key = ? AND holder_id = ?`),
		"k", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	result, err := teststore.MaintainConcurrency(ctx, s, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: time.Hour})
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.StaleWaiters) != 1 || result.StaleWaiters[0].RunID != "r1" {
		t.Fatalf("expected r1 dropped, got %+v", result.StaleWaiters)
	}
	state, _ := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if len(state.Waiters) != 0 {
		t.Fatalf("waiter should have been reaped, got %+v", state.Waiters)
	}
}

func TestConcurrency_WaiterReaperDropsOldWaiters(t *testing.T) {
	s := storetest.Open(t)
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
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE concurrency_waiters SET arrived_at = ? WHERE key = ? AND run_id = ?`),
		oneHourAgo, "k", "r1"); err != nil {
		t.Fatalf("rewrite arrived_at: %v", err)
	}

	result, err := teststore.MaintainConcurrency(ctx, s, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: 10 * time.Minute})
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(result.StaleWaiters) != 1 {
		t.Fatalf("expected 1 reaped, got %d", len(result.StaleWaiters))
	}

	acquireBareT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "w2", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	result, err = teststore.MaintainConcurrency(ctx, s, store.ConcurrencyMaintenanceOptions{WaiterMaxAge: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.StaleWaiters) != 0 {
		t.Fatalf("fresh waiter should not be reaped: %+v", result.StaleWaiters)
	}
}

var _ = context.Background
