package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	if dl, ok := t.Deadline(); ok {
		ctx, cancel := context.WithDeadline(context.Background(), dl)
		t.Cleanup(cancel)
		return ctx
	}
	return context.Background()
}

func acquireT(t *testing.T, s *store.Store, req store.AcquireSlotRequest) store.AcquireSlotResponse {
	t.Helper()
	if req.RunID != "" {
		createLiveRunT(t, s, req.RunID)
	}
	return acquireBareT(t, s, req)
}

func acquireBareT(t *testing.T, s *store.Store, req store.AcquireSlotRequest) store.AcquireSlotResponse {
	t.Helper()
	if req.RunID != "" && req.NodeID != "" && req.NodeID[0] != '#' {
		var found int
		if err := s.DB().QueryRowContext(ctxT(t), storetest.Rebind(s, "SELECT COUNT(*) FROM nodes WHERE run_id=? AND node_id=?"), req.RunID, req.NodeID).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found == 0 {
			if err := teststore.SeedNode(s, ctxT(t), store.Node{RunID: req.RunID, NodeID: req.NodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	resp, err := tenantFor(t, s, store.DefaultTeam).AcquireConcurrencySlot(ctxT(t), req)
	if err != nil {
		t.Fatalf("AcquireConcurrencySlot(%+v): %v", req, err)
	}
	return resp
}

func createLiveRunT(t *testing.T, s *store.Store, runID string) {
	t.Helper()
	ctx := ctxT(t)
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "test-pipeline",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s, "UPDATE runs SET dispatch='controller',plan_accepted_generation=1 WHERE id=?"), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO leases(name,holder,generation,until) VALUES ('controller.owner','fixture',1,4102444800000000000) ON CONFLICT(name) DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"n", "n0", "n1", "node"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, storetest.Rebind(s, "SELECT COUNT(*) FROM nodes WHERE run_id=? AND node_id=?"), runID, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			if err := teststore.SeedNode(s, ctx, store.Node{RunID: runID, NodeID: id, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestConcurrency_GrantedWhenSlotAvailable(t *testing.T) {
	s := storetest.Open(t)
	resp := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("expected Granted, got %s", resp.Kind)
	}
	if resp.LeaseExpiresAt.IsZero() {
		t.Fatalf("expected non-zero lease")
	}
}

func TestConcurrencyHolderPreservesCost(t *testing.T) {
	s := storetest.Open(t)
	resp := acquireT(t, s, store.AcquireSlotRequest{
		Key:      "k-cost",
		HolderID: "r1/n1",
		RunID:    "r1",
		NodeID:   "n1",
		Capacity: 4,
		Cost:     3,
		Policy:   store.OnLimitQueue,
	})
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("expected Granted, got %s", resp.Kind)
	}

	holder, err := tenantFor(t, s, store.DefaultTeam).ConcurrencyHolder(ctxT(t), "k-cost", "r1/n1", time.Now())
	if err != nil {
		t.Fatalf("ConcurrencyHolder: %v", err)
	}
	if holder.Cost != 3 {
		t.Fatalf("holder cost = %d, want 3", holder.Cost)
	}
	if holder.DeclaredCapacity != 4 {
		t.Fatalf("holder declared capacity = %d, want 4", holder.DeclaredCapacity)
	}
}

func TestConcurrency_QueueWhenFull(t *testing.T) {
	s := storetest.Open(t)
	r1 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r1.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r1.Kind)
	}
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r2.Kind != store.AcquireQueued {
		t.Fatalf("r2: want Queued got %s", r2.Kind)
	}
}

func finishRunT(t *testing.T, s *store.Store, runID string) {
	t.Helper()
	now := time.Now()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctxT(t), store.Run{
		ID:        runID,
		Pipeline:  "test",
		Status:    "running",
		StartedAt: now,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
	if err := teststore.SeedRunFinish(s, ctxT(t), store.DefaultTeam, runID, "success", ""); err != nil {
		t.Fatalf("FinishRun(%s): %v", runID, err)
	}
}

func TestConcurrency_AcquireReapsTerminalHolderBeforeAccounting(t *testing.T) {
	s := storetest.Open(t)
	r1 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "terminal-holder", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Hour,
	})
	if r1.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r1.Kind)
	}
	finishRunT(t, s, "r1")

	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "terminal-holder", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r2.Kind != store.AcquireGranted {
		t.Fatalf("r2: want Granted after terminal holder reap got %s", r2.Kind)
	}
	if holderExists(t, s, "terminal-holder", "r1/n1") {
		t.Fatalf("terminal holder still present")
	}
}

func TestConcurrency_ResolveWaiterReapsTerminalHolderAndPromotes(t *testing.T) {
	s := storetest.Open(t)
	r1 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "terminal-promote", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Hour,
	})
	if r1.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r1.Kind)
	}
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "terminal-promote", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r2.Kind != store.AcquireQueued {
		t.Fatalf("r2: want Queued got %s", r2.Kind)
	}
	finishRunT(t, s, "r1")

	res, err := tenantFor(t, s, store.DefaultTeam).ResolveWaiter(ctxT(t), "terminal-promote", "r2", "n1", "", "", "", false)
	if err != nil {
		t.Fatalf("ResolveWaiter: %v", err)
	}
	if res.Status != "promoted" {
		t.Fatalf("status = %s, want promoted", res.Status)
	}
	if holderExists(t, s, "terminal-promote", "r1/n1") {
		t.Fatalf("terminal holder still present")
	}
}

func TestConcurrency_Semaphore(t *testing.T) {
	s := storetest.Open(t)
	for i := range 3 {
		holder := store.AcquireSlotRequest{
			Key: "k", HolderID: id("r", i), RunID: id("r", i), NodeID: "n1",
			Capacity: 3, Policy: store.OnLimitQueue,
		}
		resp := acquireT(t, s, holder)
		if resp.Kind != store.AcquireGranted {
			t.Fatalf("arrival %d: want Granted got %s", i, resp.Kind)
		}
	}
	r4 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r4/n1", RunID: "r4", NodeID: "n1",
		Capacity: 3, Policy: store.OnLimitQueue,
	})
	if r4.Kind != store.AcquireQueued {
		t.Fatalf("r4: want Queued got %s", r4.Kind)
	}
}

func TestConcurrency_Skip(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitSkip,
	})
	if r2.Kind != store.AcquireSkipped {
		t.Fatalf("r2: want Skipped got %s", r2.Kind)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("skip created a waiter row: %+v", state.Waiters)
	}
}

func TestConcurrency_Fail(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitFail,
	})
	if r2.Kind != store.AcquireFailed {
		t.Fatalf("r2: want Failed got %s", r2.Kind)
	}
}

func TestConcurrency_CancelOthersMarksOldestSuperseded(t *testing.T) {
	s := storetest.Open(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	})
	if r2.Kind != store.AcquireCancellingOthers {
		t.Fatalf("r2: want CancellingOthers got %s", r2.Kind)
	}
	if len(r2.SupersededIDs) != 1 || r2.SupersededIDs[0] != "r1/n1" {
		t.Fatalf("r2: SupersededIDs = %v, want [r1/n1]", r2.SupersededIDs)
	}

	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	var r1Superseded, r2Holds bool
	for _, h := range state.Holders {
		if h.HolderID == "r1/n1" && h.Superseded {
			r1Superseded = true
		}
		if h.HolderID == "r2/n1" && !h.Superseded {
			r2Holds = true
		}
	}
	if !r1Superseded {
		t.Fatalf("r1/n1 should be marked superseded: %+v", state.Holders)
	}
	if !r2Holds {
		t.Fatalf("r2/n1 (canceller) should hold the slot: %+v", state.Holders)
	}
}

func TestConcurrency_DriftWarnOnCapacityChange(t *testing.T) {
	s := storetest.Open(t)
	r1 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r1.DriftNote != "" {
		t.Fatalf("first arrival should have no drift note, got: %q", r1.DriftNote)
	}
	r2 := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 3, Policy: store.OnLimitQueue,
	})
	if r2.PreviousCapacity != 1 {
		t.Fatalf("r2: PreviousCapacity = %d, want 1", r2.PreviousCapacity)
	}
	if r2.DriftNote == "" {
		t.Fatalf("r2: expected drift note, got empty")
	}
}

func TestConcurrency_PromoteNextWaiters(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	createLiveRunT(t, s, "r2")
	released, _, promoted, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "k", "r1/n1", "success", "", "", 0, 30*time.Second)
	if err != nil || !released {
		t.Fatalf("release r1: released=%v err=%v", released, err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r2" {
		t.Fatalf("promote: got %+v, want [r2]", promoted)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "r2" {
		t.Fatalf("expected r2 as holder, got %+v", state.Holders)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("waiter should have been drained, got %+v", state.Waiters)
	}
}

func TestConcurrency_PromoteSkipsAndDeletesFinishedRunWaiter(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)
	started := time.Now()
	for _, id := range []string{"r2", "r3"} {
		if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{ID: id, Pipeline: "p", Status: "running", StartedAt: started}); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
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
	if err := teststore.SeedRunFinish(s, ctx, store.DefaultTeam, "r2", "success", ""); err != nil {
		t.Fatalf("finish r2: %v", err)
	}
	released, _, promoted, err := tenantFor(t, s, store.DefaultTeam).ReleaseAndNotify(ctx, "k", "r1/n1", "success", "", "", 0, 30*time.Second)
	if err != nil || !released {
		t.Fatalf("release r1: released=%v err=%v", released, err)
	}
	if len(promoted) != 1 || promoted[0].RunID != "r3" {
		t.Fatalf("promote: got %+v, want [r3]", promoted)
	}
	state, err := tenantFor(t, s, store.DefaultTeam).GetConcurrencyState(ctx, "k")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "r3" {
		t.Fatalf("expected r3 as the only holder, got %+v", state.Holders)
	}
	for _, w := range state.Waiters {
		if w.RunID == "r2" {
			t.Fatalf("finished-run waiter r2 should have been deleted, still queued: %+v", state.Waiters)
		}
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("queue should be drained, got %+v", state.Waiters)
	}
}

func TestConcurrency_HeartbeatReportsSuperseded(t *testing.T) {
	s := storetest.Open(t)
	ctx := ctxT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n1", RunID: "r1", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	_, superseded, err := tenantFor(t, s, store.DefaultTeam).HeartbeatConcurrencySlot(ctx, "k", "r1/n1", 10*time.Second)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if superseded {
		t.Fatalf("first heartbeat should not report superseded")
	}
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n1", RunID: "r2", NodeID: "n1",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	})
	_, superseded, err = tenantFor(t, s, store.DefaultTeam).HeartbeatConcurrencySlot(ctx, "k", "r1/n1", 10*time.Second)
	if err != nil {
		t.Fatalf("heartbeat after cancel: %v", err)
	}
	if !superseded {
		t.Fatalf("heartbeat after CancelOthers should report superseded=true")
	}
}

func id(prefix string, i int) string {
	return prefix + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
