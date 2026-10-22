package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestConcurrency_CostWeightedAdmission(t *testing.T) {
	s := storetest.Open(t)
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

	promoted := releaseAndPromoteT(t, s, "db", "r1/n")
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("expected r3 promoted, got %+v", promoted)
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func releaseAndPromoteT(t *testing.T, s *store.Store, key, holderID string) []store.ConcurrencyWaiter {
	t.Helper()
	_, _, promoted, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctxT(t), key, holderID, "success", "", "", 0, 0)
	if err != nil {
		t.Fatalf("ReleaseAndNotify(%s,%s): %v", key, holderID, err)
	}
	return promoted
}

func TestConcurrency_CostHeavyWaiterHoldsFIFO(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r.Kind)
	}
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r2/n", RunID: "r2", NodeID: "n",
		Capacity: 4, Cost: 4, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "r3/n", RunID: "r3", NodeID: "n",
		Capacity: 4, Cost: 1, Policy: store.OnLimitQueue,
	})

	promoted := releaseAndPromoteT(t, s, "db", "r1/n")
	if len(promoted) != 1 || promoted[0].RunID != "r2" {
		t.Fatalf("expected only r2 promoted once full budget freed, got %+v", promoted)
	}
}

func TestConcurrency_CostBackfillsBehindNonFittingHeavyWaiter(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "holder/n", RunID: "holder", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("holder: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want Queued got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "light/n", RunID: "light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("light: want Granted as backfill got %s", r.Kind)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if state.UsedCost != 8 || len(state.Holders) != 2 || len(state.Waiters) != 1 || state.Waiters[0].RunID != "heavy" {
		t.Fatalf("state after backfill = used %d holders %+v waiters %+v, want used=8 holder+light and heavy queued",
			state.UsedCost, state.Holders, state.Waiters)
	}
	if promoted := releaseAndPromoteT(t, s, "db", "holder/n"); len(promoted) != 1 || promoted[0].RunID != "heavy" {
		t.Fatalf("expected heavy promoted after holder releases, got %+v", promoted)
	}
}

func TestConcurrency_CostPromotionBackfillsBehindNonFittingHeavyWaiter(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "holder-heavy/n", RunID: "holder-heavy", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("holder-heavy: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "holder-light/n", RunID: "holder-light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("holder-light: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want Queued got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "light/n", RunID: "light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("light: want Queued got %s", r.Kind)
	}

	if promoted := releaseAndPromoteT(t, s, "db", "holder-light/n"); len(promoted) != 1 || promoted[0].RunID != "light" {
		t.Fatalf("expected light promoted behind non-fitting heavy, got %+v", promoted)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "heavy" {
		t.Fatalf("waiters after backfill promotion = %+v, want only heavy", state.Waiters)
	}
}

func TestConcurrency_CostBackfillStopsWhenYoungerHolderBlocksOldestWaiter(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	s := storetest.Open(t, teststore.WithClock(func() time.Time { return now }))
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "older/n", RunID: "older", NodeID: "n",
		Capacity: 10, Cost: 5, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("older: want Granted got %s", r.Kind)
	}
	now = now.Add(time.Nanosecond)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want Queued got %s", r.Kind)
	}
	now = now.Add(time.Nanosecond)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "light-1/n", RunID: "light-1", NodeID: "n",
		Capacity: 10, Cost: 5, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("light-1: want Granted as backfill got %s", r.Kind)
	}
	now = now.Add(time.Nanosecond)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "light-2/n", RunID: "light-2", NodeID: "n",
		Capacity: 10, Cost: 5, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("light-2: want Queued behind protected heavy got %s", r.Kind)
	}
	if promoted := releaseAndPromoteT(t, s, "db", "older/n"); len(promoted) != 0 {
		t.Fatalf("expected no promotion while younger holder blocks heavy, got %+v", promoted)
	}
	if promoted := releaseAndPromoteT(t, s, "db", "light-1/n"); len(promoted) != 1 || promoted[0].RunID != "heavy" {
		t.Fatalf("expected heavy promoted once younger holder releases, got %+v", promoted)
	}
}

func TestConcurrency_CostBackfillDeletesDeadEarlierWaiter(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "holder/n", RunID: "holder", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("holder: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "dead/n", RunID: "dead", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("dead: want Queued got %s", r.Kind)
	}
	if err := teststore.SeedRunFinish(s, ctxT(t), store.DefaultTeam, "dead", "failed", "test terminal run"); err != nil {
		t.Fatalf("FinishRun(dead): %v", err)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "light/n", RunID: "light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("light: want Granted after dead waiter cleanup got %s", r.Kind)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("dead waiter was not removed: %+v", state.Waiters)
	}
}

func TestConcurrency_MostRestrictiveCapacityWins(t *testing.T) {
	s := storetest.Open(t)
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 5, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("A: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 2, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "db", HolderID: "rC/n", RunID: "rC", NodeID: "n",
		Capacity: 5, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("C: want Queued under effective cap 2, got %s", r.Kind)
	}

	promoted := releaseAndPromoteT(t, s, "db", "rB/n")
	if len(promoted) != 1 || promoted[0].RunID != "rC" {
		t.Fatalf("expected rC promoted once the cap-2 participant drained, got %+v", promoted)
	}
}
