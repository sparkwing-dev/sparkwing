package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var waitingCancelStarted chan struct{}

type cancelWhileWaiting struct{ sparkwing.Base }

func (cancelWhileWaiting) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	blocker := sparkwing.Job(plan, "blocker", func(ctx context.Context) error {
		close(waitingCancelStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	sparkwing.Job(plan, "waiter", func(ctx context.Context) error { return nil }).Needs(blocker)
	return nil
}

func init() {
	register("orch-cancel-waiting", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &cancelWhileWaiting{} })
}

func TestRun_CancelWhileWaitingOnDependencyRecordsTerminalNode(t *testing.T) {
	p := newPaths(t)
	waitingCancelStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-waitingCancelStarted
		cancel()
	}()

	res, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "orch-cancel-waiting"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	waiter, err := st.GetNode(context.Background(), res.RunID, "waiter")
	if err != nil || waiter == nil {
		t.Fatalf("get waiter node: %v", err)
	}
	if waiter.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("waiter outcome = %q, status = %q, want outcome cancelled: the run is terminal, so the node it never dispatched must be terminal too",
			waiter.Outcome, waiter.Status)
	}
	if waiter.Status == "pending" {
		t.Fatalf("waiter status = %q, want a finished status", waiter.Status)
	}
}
