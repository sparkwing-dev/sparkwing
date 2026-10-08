package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func startTestLoopbackShim(t *testing.T) (*store.Store, *loopbackController) {
	t.Helper()
	paths := PathsAt(t.TempDir())
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	shim, err := startLoopbackShim(backends.State, backends.Concurrency, "run-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shim.Close)
	return st, shim
}

func TestLoopbackShimServesALocalNodesBounceAndAttemptRoutes(t *testing.T) {
	st, shim := startTestLoopbackShim(t)
	c := client.NewWithToken(shim.url, nil, shim.token)
	ctx := context.Background()
	if b, err := c.PendingNodeBounce(ctx, "run-1", "build"); err != nil || b != nil {
		t.Fatalf("bounce poll = %+v, %v; want none pending", b, err)
	}
	if err := c.ConsumeNodeBounce(ctx, "run-1", "build", 1, store.BounceBounced); err == nil {
		t.Fatal("consumed a bounce request no backend holds")
	}
	start := store.ExecutionStart{AttemptOrdinal: 1, ExecutorKind: store.ExecutorKindLocal, ExecutorID: "laptop"}
	if err := c.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", start); err != nil {
		t.Fatalf("execution-start on the shim: %v", err)
	}
	finish := store.ExecutionAttemptFinish{AttemptOrdinal: 1, Outcome: "success", ExecutorKind: store.ExecutorKindLocal}
	if err := c.FinishNodeExecutionAttempt(ctx, "run-1", "build", finish); err != nil {
		t.Fatalf("execution-finish on the shim: %v", err)
	}
	attempts, err := st.ListNodeExecutionAttempts(ctx, "run-1", "build")
	if err != nil || len(attempts) != 1 || attempts[0].FinishedAt == nil {
		t.Fatalf("recorded attempts = %+v, %v", attempts, err)
	}
}

func TestLoopbackShimGrantsSlotsToASpawnedNode(t *testing.T) {
	_, shim := startTestLoopbackShim(t)
	conc := NewHTTPConcurrency(shim.url, nil, shim.token, time.Minute)
	ctx := context.Background()
	got, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-1/build/linux", RunID: "run-1", NodeID: "build/linux", Capacity: 1, Lease: time.Minute,
	})
	if err != nil || got.Kind != store.AcquireGranted {
		t.Fatalf("acquire on the shim = %+v, %v", got, err)
	}
	if holder, err := conc.ObserveSlot(ctx, "deploy", "run-1/build/linux"); err != nil || holder.NodeID != "build/linux" {
		t.Fatalf("observe = %+v, %v", holder, err)
	}
	if _, superseded, err := conc.HeartbeatSlot(ctx, "deploy", "run-1/build/linux", time.Minute); err != nil || superseded {
		t.Fatalf("heartbeat = %v, %v", superseded, err)
	}
	queued, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-1/build/darwin", RunID: "run-1", NodeID: "build/darwin", Capacity: 1, Lease: time.Minute,
	})
	if err != nil || queued.Kind != store.AcquireQueued {
		t.Fatalf("second acquire = %+v, %v", queued, err)
	}
	if res, err := conc.ResolveWaiter(ctx, "deploy", "run-1", "build/darwin", "", "", "", false); err != nil || res.Status == "" {
		t.Fatalf("resolve = %+v, %v", res, err)
	}
	if cancelled, err := conc.CancelWaiter(ctx, "deploy", "run-1", "build/darwin"); err != nil || !cancelled {
		t.Fatalf("cancel waiter = %v, %v", cancelled, err)
	}
	if _, err := conc.ForceReleaseSuperseded(ctx, "deploy"); err != nil {
		t.Fatalf("force-release: %v", err)
	}
	if err := conc.ReleaseSlot(ctx, "deploy", "run-1/build/linux", "success", "", "", 0); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-2/build", RunID: "run-2", NodeID: "build", Capacity: 1, Lease: time.Minute,
	}); err == nil {
		t.Fatal("the shim granted a slot to another run")
	}
}

func TestLoopbackShimRefusesToJoinAnotherHolder(t *testing.T) {
	st, shim := startTestLoopbackShim(t)
	ctx := context.Background()
	held, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "deploy", HolderID: "run-2/build", RunID: "run-2", NodeID: "build", Capacity: 1, Lease: time.Minute,
	})
	if err != nil || held.Kind != store.AcquireGranted {
		t.Fatalf("seed run-2's holder = %+v, %v", held, err)
	}
	conc := NewHTTPConcurrency(shim.url, nil, shim.token, time.Minute)
	for _, inherited := range []string{"run-2/build", "run-1/build"} {
		if got, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
			Key: "deploy", HolderID: "run-1/build", InheritedHolderID: inherited, RunID: "run-1", NodeID: "build",
			Capacity: 1, Lease: time.Hour,
		}); err == nil {
			t.Fatalf("joined %s through the shim: %+v", inherited, got)
		}
	}
	state, err := st.GetConcurrencyState(ctx, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 || state.Holders[0].HolderID != "run-2/build" ||
		!state.Holders[0].LeaseExpiresAt.Equal(held.LeaseExpiresAt) {
		t.Fatalf("holders after the refused joins = %+v, want run-2's holder untouched", state.Holders)
	}
}
