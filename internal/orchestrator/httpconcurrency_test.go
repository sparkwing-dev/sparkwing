package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

	res, err := b.ResolveWaiter(context.Background(), "db", "r3", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve (queued): %v", err)
	}
	if res.Status != store.WaiterStillWaiting {
		t.Fatalf("r3 resolve = %s, want still_waiting", res.Status)
	}

	if err := b.ReleaseSlot(context.Background(), "db", "r1/n", "success", "", "", 0); err != nil {
		t.Fatalf("release r1: %v", err)
	}
	res, err = b.ResolveWaiter(context.Background(), "db", "r3", "n", "", "", "", false)
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
	if r := acquireHTTP(t, b, mk("rC", 5)); r.Kind != store.AcquireQueued {
		t.Fatalf("C: want Queued under effective cap 2, got %s", r.Kind)
	}
	if err := b.ReleaseSlot(context.Background(), "db", "rB/n", "success", "", "", 0); err != nil {
		t.Fatalf("release B: %v", err)
	}
	res, err := b.ResolveWaiter(context.Background(), "db", "rC", "n", "", "", "", false)
	if err != nil {
		t.Fatalf("resolve C: %v", err)
	}
	if res.Status != store.WaiterPromoted {
		t.Fatalf("C resolve = %s, want promoted once the cap-2 participant drained", res.Status)
	}
}

func TestHTTPConcurrency_ScopeQualifiedKeysAreIndependent(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
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
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "g@run-1", HolderID: "run-1/m", RunID: "run-1", NodeID: "m",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("run-1/m: want Queued on the same scoped key got %s", r.Kind)
	}
}

func TestHTTPConcurrency_CancelWaiterAndForceRelease(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
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
	if _, err := b.ForceReleaseSuperseded(context.Background(), "k"); err != nil {
		t.Fatalf("force release: %v", err)
	}
}

// Defect 4: --no-cache (BypassRead) must reach the store over the HTTP
// wire. After a cache entry exists, a BypassRead acquire must skip the
// cache read and run fresh (Granted), not replay the stale entry.
func TestParity_BypassRead_NoCache(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	ctx := context.Background()
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "memo:h", HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "h", CacheTTL: time.Hour,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("leader: want Granted got %s", r.Kind)
	}
	if err := b.ReleaseSlot(ctx, "memo:h", "r1/n", "success", "r1/n", "h", time.Hour); err != nil {
		t.Fatalf("release: %v", err)
	}
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "memo:h", HolderID: "r2/n", RunID: "r2", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "h",
	}); r.Kind != store.AcquireCached {
		t.Fatalf("sanity: want Cached got %s", r.Kind)
	}
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "memo:h", HolderID: "r3/n", RunID: "r3", NodeID: "n",
		Capacity: 1, Cost: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: "h", BypassRead: true,
	}); r.Kind == store.AcquireCached {
		t.Fatalf("BypassRead acquire returned Cached; --no-cache was dropped on the wire")
	}
}

// Defect 8: a queued acquire's Position/QueueLength/Holders must cross
// the HTTP wire so the dashboard renders the real queue depth.
func TestParity_QueuedAcquire_Position_QueueLength_Holders(t *testing.T) {
	b, _ := newHTTPConcurrency(t)
	if r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("A: want Granted got %s", r.Kind)
	}
	r := acquireHTTP(t, b, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r.Kind != store.AcquireQueued {
		t.Fatalf("B: want Queued got %s", r.Kind)
	}
	if r.QueueLength != 1 {
		t.Fatalf("queued acquire QueueLength = %d, want 1 (dropped on the wire)", r.QueueLength)
	}
	if len(r.Holders) != 1 || r.Holders[0].RunID != "rA" {
		t.Fatalf("queued acquire Holders = %+v, want the rA holder (dropped on the wire)", r.Holders)
	}
}
