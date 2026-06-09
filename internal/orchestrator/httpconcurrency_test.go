package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The hosted-controller path uses HTTPConcurrency, which proxies the
// concurrency backend over /api/v1/concurrency/*. These tests confirm
// the chunk-2 engine semantics -- cost-weighted admission,
// most-restrictive capacity, scope-qualified keys, and waiter
// resolution -- hold across the wire, not only in-process.

func newHTTPConcurrency(t *testing.T) (*orchestrator.HTTPConcurrency, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ts := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(ts.Close)
	return orchestrator.NewHTTPConcurrency(ts.URL, nil, "", 0), st
}

func acquireHTTP(t *testing.T, b *orchestrator.HTTPConcurrency, req store.AcquireSlotRequest) store.AcquireSlotResponse {
	t.Helper()
	resp, err := b.AcquireSlot(context.Background(), req)
	if err != nil {
		t.Fatalf("AcquireSlot(%s/%s): %v", req.Key, req.HolderID, err)
	}
	return resp
}

func TestHTTPConcurrency_CostWeightedAdmission(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	mk := func(run string) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: "db", HolderID: run + "/n", RunID: run, NodeID: "n",
			Capacity: 8, Cost: 4, Policy: store.OnLimitQueue,
		}
	}
	if r := acquireHTTP(t, b, mk("r1")); r.Kind != store.AcquireGranted {
		t.Fatalf("r1: want Granted got %s", r.Kind)
	}
	if r := acquireHTTP(t, b, mk("r2")); r.Kind != store.AcquireGranted {
		t.Fatalf("r2: want Granted (4+4<=8) got %s", r.Kind)
	}
	if r := acquireHTTP(t, b, mk("r3")); r.Kind != store.AcquireQueued {
		t.Fatalf("r3: want Queued (8+4>8) got %s", r.Kind)
	}

	// Resolve while still queued.
	res, err := b.ResolveWaiter(context.Background(), "db", "r3", "n", "", "", "")
	if err != nil {
		t.Fatalf("resolve (queued): %v", err)
	}
	if res.Status != store.WaiterStillWaiting {
		t.Fatalf("r3 resolve = %s, want still_waiting", res.Status)
	}

	// Drain r1; the queued cost-4 member now fits and is promoted.
	if err := b.ReleaseSlot(context.Background(), "db", "r1/n", "success", "", "", 0); err != nil {
		t.Fatalf("release r1: %v", err)
	}
	res, err = b.ResolveWaiter(context.Background(), "db", "r3", "n", "", "", "")
	if err != nil {
		t.Fatalf("resolve (after release): %v", err)
	}
	if res.Status != store.WaiterPromoted {
		t.Fatalf("r3 resolve = %s, want promoted", res.Status)
	}
	if res.HolderID == "" {
		t.Fatalf("promoted waiter has no holder id")
	}
}

func TestHTTPConcurrency_MostRestrictiveWins(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	mk := func(run string, cap int) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: "db", HolderID: run + "/n", RunID: run, NodeID: "n",
			Capacity: cap, Cost: 1, Policy: store.OnLimitQueue,
		}
	}
	if r := acquireHTTP(t, b, mk("rA", 5)); r.Kind != store.AcquireGranted {
		t.Fatalf("A: want Granted got %s", r.Kind)
	}
	if r := acquireHTTP(t, b, mk("rB", 2)); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted got %s", r.Kind)
	}
	// Effective capacity is min(5,2,5)=2; C queues despite declaring 5.
	if r := acquireHTTP(t, b, mk("rC", 5)); r.Kind != store.AcquireQueued {
		t.Fatalf("C: want Queued under effective cap 2, got %s", r.Kind)
	}
	// Drain the restrictive participant; effective rises to 5, C promotes.
	if err := b.ReleaseSlot(context.Background(), "db", "rB/n", "success", "", "", 0); err != nil {
		t.Fatalf("release B: %v", err)
	}
	res, err := b.ResolveWaiter(context.Background(), "db", "rC", "n", "", "", "")
	if err != nil {
		t.Fatalf("resolve C: %v", err)
	}
	if res.Status != store.WaiterPromoted {
		t.Fatalf("C resolve = %s, want promoted once the cap-2 participant drained", res.Status)
	}
}

func TestHTTPConcurrency_ScopeQualifiedKeysAreIndependent(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	// Two run-scoped keys (name@runID) share capacity 1 each but are
	// independent of one another -- both grant.
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "g@run-1", HolderID: "run-1/n", RunID: "run-1", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("run-1: want Granted got %s", r.Kind)
	}
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "g@run-2", HolderID: "run-2/n", RunID: "run-2", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("run-2: want Granted (independent scoped key) got %s", r.Kind)
	}
	// A second arrival on the same scoped key queues.
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "g@run-1", HolderID: "run-1/m", RunID: "run-1", NodeID: "m",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("run-1/m: want Queued on the same scoped key got %s", r.Kind)
	}
}

func TestHTTPConcurrency_CancelWaiterAndForceRelease(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	// Holder + queued waiter.
	acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n", RunID: "r1", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n", RunID: "r2", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	cancelled, err := b.CancelWaiter(context.Background(), "k", "r2", "n")
	if err != nil {
		t.Fatalf("cancel waiter: %v", err)
	}
	if !cancelled {
		t.Fatalf("expected the queued waiter to be cancelled")
	}
	// ForceReleaseSuperseded is a no-op here (no superseded holders) but
	// must round-trip without error over the wire.
	if _, err := b.ForceReleaseSuperseded(context.Background(), "k"); err != nil {
		t.Fatalf("force release: %v", err)
	}
}
