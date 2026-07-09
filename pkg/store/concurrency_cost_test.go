package store_test

import (
	"testing"
	"time"

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

	promoted := releaseAndPromoteT(t, s, "db", "r1/n")
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("expected r3 promoted, got %+v", promoted)
	}
}

func TestConcurrency_InheritedHolderExtendsAdmissionWithoutRechargingCost(t *testing.T) {
	s := newStoreT(t)
	parent := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}

	child := acquireT(t, s, store.AcquireSlotRequest{
		Key:               "db",
		HolderID:          "child/-",
		InheritedHolderID: "parent/-",
		RunID:             "child",
		Capacity:          10,
		Cost:              20,
		Policy:            store.OnLimitQueue,
		Lease:             2 * time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child: want Granted got %s", child.Kind)
	}
	if child.HolderID != "child/-" {
		t.Fatalf("child holder = %q, want child holder", child.HolderID)
	}

	state, err := s.GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if state.UsedCost != 8 {
		t.Fatalf("used cost = %d, want 8 (child join must not recharge)", state.UsedCost)
	}
	if len(state.Holders) != 2 || state.Holders[0].HolderID != "parent/-" || state.Holders[1].HolderID != "child/-" {
		t.Fatalf("holders = %+v, want parent and zero-cost child holder", state.Holders)
	}

	holder, err := s.ConcurrencyHolder(ctxT(t), "db", "parent/-", time.Now())
	if err != nil {
		t.Fatalf("ConcurrencyHolder: %v", err)
	}
	if !holder.LeaseExpiresAt.After(parent.LeaseExpiresAt) {
		t.Fatalf("inherited join did not extend parent lease: before=%s after=%s",
			parent.LeaseExpiresAt, holder.LeaseExpiresAt)
	}
	childHolder, err := s.ConcurrencyHolder(ctxT(t), "db", "child/-", time.Now())
	if err != nil {
		t.Fatalf("ConcurrencyHolder(child): %v", err)
	}
	if childHolder.NodeID != "" {
		t.Fatalf("child node id before parent release = %q, want empty plan holder node", childHolder.NodeID)
	}

	if _, err := s.ReleaseConcurrencySlot(ctxT(t), "db", "parent/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseConcurrencySlot(parent): %v", err)
	}
	state, err = s.GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState after parent release: %v", err)
	}
	if state.UsedCost != 8 {
		t.Fatalf("used cost after parent release = %d, want transferred 8", state.UsedCost)
	}
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "child/-" || state.Holders[0].Cost != 8 {
		t.Fatalf("holders after parent release = %+v, want child carrying parent cost", state.Holders)
	}
	if state.Holders[0].NodeID != "" {
		t.Fatalf("child node id after parent release = %q, want empty plan holder node", state.Holders[0].NodeID)
	}
	childHolder, err = s.ConcurrencyHolder(ctxT(t), "db", "child/-", time.Now())
	if err != nil {
		t.Fatalf("ConcurrencyHolder(child after parent release): %v", err)
	}
	if childHolder.NodeID != "" {
		t.Fatalf("observed child node id after parent release = %q, want empty plan holder node", childHolder.NodeID)
	}

	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "follower/-",
		RunID:    "follower",
		Capacity: 10,
		Cost:     3,
		Policy:   store.OnLimitQueue,
	})
	if follower.Kind != store.AcquireQueued {
		t.Fatalf("follower: want Queued because parent still accounts for cost 8, got %s", follower.Kind)
	}
}

func TestConcurrency_TerminalReaperTransfersInheritedHolderCost(t *testing.T) {
	s := newStoreT(t)
	createRunningRunT(t, s, "parent")
	createRunningRunT(t, s, "child")
	parent := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	child := acquireT(t, s, store.AcquireSlotRequest{
		Key:               "db",
		HolderID:          "child/-",
		InheritedHolderID: "parent/-",
		RunID:             "child",
		Capacity:          10,
		Cost:              20,
		Policy:            store.OnLimitQueue,
		Lease:             2 * time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child: want Granted got %s", child.Kind)
	}
	finishRunT(t, s, "parent")

	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "follower/-",
		RunID:    "follower",
		Capacity: 10,
		Cost:     3,
		Policy:   store.OnLimitQueue,
	})
	if follower.Kind != store.AcquireQueued {
		t.Fatalf("follower: want Queued after inherited cost transfer, got %s", follower.Kind)
	}
	state, err := s.GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if state.UsedCost != 8 {
		t.Fatalf("used cost = %d, want transferred 8", state.UsedCost)
	}
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "child/-" || state.Holders[0].Cost != 8 {
		t.Fatalf("holders = %+v, want child carrying transferred parent cost", state.Holders)
	}
}

func TestConcurrency_ReleaseTransfersCostToSiblingInheritedHolder(t *testing.T) {
	s := newStoreT(t)
	parent := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	for _, childRunID := range []string{"child-a", "child-b"} {
		child := acquireT(t, s, store.AcquireSlotRequest{
			Key:               "db",
			HolderID:          childRunID + "/-",
			InheritedHolderID: "parent/-",
			RunID:             childRunID,
			Capacity:          10,
			Cost:              20,
			Policy:            store.OnLimitQueue,
			Lease:             2 * time.Minute,
		})
		if child.Kind != store.AcquireGranted {
			t.Fatalf("%s: want Granted got %s", childRunID, child.Kind)
		}
	}
	if _, err := s.ReleaseConcurrencySlot(ctxT(t), "db", "parent/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseConcurrencySlot(parent): %v", err)
	}
	if _, err := s.ReleaseConcurrencySlot(ctxT(t), "db", "child-a/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseConcurrencySlot(child-a): %v", err)
	}

	follower := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "follower/-",
		RunID:    "follower",
		Capacity: 10,
		Cost:     3,
		Policy:   store.OnLimitQueue,
	})
	if follower.Kind != store.AcquireQueued {
		t.Fatalf("follower: want Queued while child-b carries inherited cost, got %s", follower.Kind)
	}
	state, err := s.GetConcurrencyState(ctxT(t), "db")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if state.UsedCost != 8 {
		t.Fatalf("used cost = %d, want transferred sibling cost 8", state.UsedCost)
	}
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "child-b/-" || state.Holders[0].Cost != 8 {
		t.Fatalf("holders = %+v, want child-b carrying transferred parent cost", state.Holders)
	}
}

func TestConcurrency_CancelOthersSupersedesSiblingInheritedHolders(t *testing.T) {
	s := newStoreT(t)
	parent := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	for _, childRunID := range []string{"child-a", "child-b"} {
		child := acquireT(t, s, store.AcquireSlotRequest{
			Key:               "db",
			HolderID:          childRunID + "/-",
			InheritedHolderID: "parent/-",
			RunID:             childRunID,
			Capacity:          10,
			Cost:              20,
			Policy:            store.OnLimitQueue,
			Lease:             2 * time.Minute,
		})
		if child.Kind != store.AcquireGranted {
			t.Fatalf("%s: want Granted got %s", childRunID, child.Kind)
		}
	}
	if _, err := s.ReleaseConcurrencySlot(ctxT(t), "db", "parent/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseConcurrencySlot(parent): %v", err)
	}

	evictor := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "evictor/-",
		RunID:    "evictor",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitCancelOthers,
		Lease:    time.Minute,
	})
	if evictor.Kind != store.AcquireCancellingOthers {
		t.Fatalf("evictor: want CancellingOthers got %s", evictor.Kind)
	}
	if !containsString(evictor.SupersededIDs, "child-a/-") || !containsString(evictor.SupersededIDs, "child-b/-") {
		t.Fatalf("superseded ids = %+v, want both inherited siblings", evictor.SupersededIDs)
	}
}

func createRunningRunT(t *testing.T, s *store.Store, runID string) {
	t.Helper()
	now := time.Now()
	if err := s.CreateRun(ctxT(t), store.Run{
		ID:        runID,
		Pipeline:  "test",
		Status:    "running",
		StartedAt: now,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
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

func TestConcurrency_CancelOthersSupersedesInheritedHolder(t *testing.T) {
	s := newStoreT(t)
	parent := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	child := acquireT(t, s, store.AcquireSlotRequest{
		Key:               "db",
		HolderID:          "child/-",
		InheritedHolderID: "parent/-",
		RunID:             "child",
		Capacity:          10,
		Cost:              8,
		Policy:            store.OnLimitQueue,
		Lease:             time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child: want Granted got %s", child.Kind)
	}

	evictor := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "db",
		HolderID: "evictor/-",
		RunID:    "evictor",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitCancelOthers,
		Lease:    time.Minute,
	})
	if evictor.Kind != store.AcquireCancellingOthers {
		t.Fatalf("evictor: want CancellingOthers got %s", evictor.Kind)
	}
	if len(evictor.SupersededIDs) != 2 || evictor.SupersededIDs[0] != "parent/-" || evictor.SupersededIDs[1] != "child/-" {
		t.Fatalf("superseded ids = %v, want parent and child", evictor.SupersededIDs)
	}
	_, superseded, err := s.HeartbeatConcurrencySlot(ctxT(t), "db", "child/-", time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatConcurrencySlot(child): %v", err)
	}
	if !superseded {
		t.Fatal("child heartbeat did not report superseded")
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

// Most-restrictive-wins: when live participants declare different
// capacities, the effective capacity is the minimum. A higher
// declaration cannot overcommit while a lower one is live; it takes
// effect only after the lower drains.
func TestConcurrency_MostRestrictiveCapacityWins(t *testing.T) {
	s := newStoreT(t)
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
