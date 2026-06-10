package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type cqHolderPipe struct{ sparkwing.Base }

func (cqHolderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cqkey", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
	sparkwing.Job(plan, "hold", semStep(1500*time.Millisecond)).Concurrency(g)
	return nil
}

type cqSurvivorPipe struct{ sparkwing.Base }

func (cqSurvivorPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("cqkey", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
	sparkwing.Job(plan, "work", semStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

func init() {
	register("cq-holder", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cqHolderPipe{} })
	register("cq-cancelled", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cqSurvivorPipe{} })
	register("cq-survivor", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cqSurvivorPipe{} })
}

// One holder, two queued waiters. Cancel one queued waiter mid-wait;
// the survivor must still get promoted and run its body, and the
// cancelled run must leave no live holder on the key.
func TestGroupedNode_CancelOneQueued_SurvivorStillPromotes(t *testing.T) {
	resetSem()
	p := newPaths(t)

	holderDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cq-holder", RunID: "cq-holder"})
		close(holderDone)
	}()
	time.Sleep(250 * time.Millisecond) // holder acquires the only slot

	// Two waiters queue behind the holder.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelledDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(cancelCtx, p, orchestrator.Options{Pipeline: "cq-cancelled", RunID: "cq-cancelled"})
		close(cancelledDone)
	}()

	survivorDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cq-survivor", RunID: "cq-survivor"})
		close(survivorDone)
	}()

	time.Sleep(300 * time.Millisecond) // both waiters park

	startRuns := sem.runs.Load() // only the holder body should have run so far
	cancel()                     // cancel one queued waiter
	<-cancelledDone

	// Holder finishes ~1.5s in; survivor must then promote and run.
	select {
	case <-survivorDone:
	case <-time.After(8 * time.Second):
		t.Fatalf("survivor never completed; a cancelled sibling stranded the queue")
	}
	<-holderDone

	// Survivor body must have executed (runs incremented beyond the
	// holder's single run captured before cancel).
	if got := sem.runs.Load(); got <= startRuns {
		t.Fatalf("survivor body did not run after sibling cancel: runs=%d startRuns=%d", got, startRuns)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()

	// Survivor node must be Success.
	nodes, _ := st.ListNodes(context.Background(), "cq-survivor")
	if len(nodes) != 1 {
		t.Fatalf("survivor: expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Fatalf("survivor node outcome = %q, want success", nodes[0].Outcome)
	}

	// No live holder belonging to the cancelled run, and at most one live
	// holder overall (the survivor may still hold briefly, but by now its
	// 50ms body is long done).
	state, err := st.GetConcurrencyState(context.Background(), "g:cqkey")
	if err != nil {
		return // reaped entirely is fine
	}
	now := time.Now()
	for _, h := range state.Holders {
		if h.Superseded || !h.LeaseExpiresAt.After(now) {
			continue
		}
		if h.RunID == "cq-cancelled" {
			t.Fatalf("cancelled queued waiter became a phantom holder: %+v", h)
		}
	}
}
