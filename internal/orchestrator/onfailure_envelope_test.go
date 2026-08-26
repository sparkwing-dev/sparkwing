package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// An OnFailure recovery node is dispatched through the runner like any
// other node, so it carries the same envelope: the cache lookup, the
// concurrency slot, and SkipIf. A pod has always run recovery nodes
// that way -- RunNodeOnce is the whole envelope and nothing about it
// is conditional on the node being a recovery node -- while the local
// dispatcher used to short-circuit straight to the body. These three
// tests pin the envelope the local path now applies.

var recoveryMemoizeRuns atomic.Int32

type onFailureMemoizePipe struct{ sparkwing.Base }

func (onFailureMemoizePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	deploy := sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("rollback", func(ctx context.Context) error {
		recoveryMemoizeRuns.Add(1)
		return nil
	})
	deploy.OnFailureNode().Memoize(
		func(ctx context.Context) sparkwing.CacheKey { return "rollback-pinned" },
		sparkwing.TTL(time.Hour))
	return nil
}

var recoverySkipIfRuns atomic.Int32

type onFailureSkipIfPipe struct{ sparkwing.Base }

func (onFailureSkipIfPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	deploy := sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		return errors.New("deploy failed")
	}).OnFailure("rollback", func(ctx context.Context) error {
		recoverySkipIfRuns.Add(1)
		return nil
	})
	deploy.OnFailureNode().SkipIf(func(ctx context.Context) bool { return true })
	return nil
}

// recoveryHolderGate lets the test hold the group's only slot while
// the recovery node asks for it, then release it so the run can end.
type recoveryHolderGate struct {
	acquired    chan struct{}
	release     chan struct{}
	acquireOnce sync.Once
	releaseOnce sync.Once
}

var activeRecoveryHolderGate atomic.Pointer[recoveryHolderGate]

func (g *recoveryHolderGate) letGo() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func installRecoveryHolderGate(t *testing.T) *recoveryHolderGate {
	t.Helper()
	gate := &recoveryHolderGate{acquired: make(chan struct{}), release: make(chan struct{})}
	if !activeRecoveryHolderGate.CompareAndSwap(nil, gate) {
		t.Fatal("recovery holder gate already installed")
	}
	t.Cleanup(func() {
		gate.letGo()
		activeRecoveryHolderGate.Store(nil)
	})
	return gate
}

var recoveryConcurrencyRuns atomic.Int32

type onFailureConcurrencyPipe struct{ sparkwing.Base }

func (onFailureConcurrencyPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("onfailure-envelope-key", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeRun,
		OnLimit:  sparkwing.Fail,
	})
	sparkwing.Job(plan, "hold", func(ctx context.Context) error {
		gate := activeRecoveryHolderGate.Load()
		if gate == nil {
			return nil
		}
		gate.acquireOnce.Do(func() { close(gate.acquired) })
		select {
		case <-gate.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).Concurrency(g)

	deploy := sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		if gate := activeRecoveryHolderGate.Load(); gate != nil {
			select {
			case <-gate.acquired:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.New("deploy failed")
	}).OnFailure("rollback", func(ctx context.Context) error {
		recoveryConcurrencyRuns.Add(1)
		return nil
	})
	deploy.OnFailureNode().Concurrency(g)
	return nil
}

func init() {
	register("mod-onfailure-memoize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureMemoizePipe{} })
	register("mod-onfailure-skipif", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureSkipIfPipe{} })
	register("mod-onfailure-concurrency", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onFailureConcurrencyPipe{} })
}

// TestOnFailure_RecoveryNodeMemoizes pins that a recovery node
// declaring Memoize gets the cache lookup, so a second identical
// failure replays the stored rollback instead of running it again.
//
// This is the behavior to want, not merely the behavior that falls
// out: an author who writes Memoize on a recovery node opted that node
// into memoization the same way they would any other node, and an
// author who wants the rollback to run every time simply does not
// declare it. Making recovery the one node kind whose Memoize is
// silently ignored would be the surprising rule, and it is not the
// rule a pod follows.
func TestOnFailure_RecoveryNodeMemoizes(t *testing.T) {
	recoveryMemoizeRuns.Store(0)
	p := newPaths(t)

	first, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "mod-onfailure-memoize"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "failed" {
		t.Fatalf("first run status = %q, want failed (the parent failed)", first.Status)
	}
	if got := recoveryMemoizeRuns.Load(); got != 1 {
		t.Fatalf("rollback body ran %d times on the first run, want 1", got)
	}

	second, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "mod-onfailure-memoize"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := recoveryMemoizeRuns.Load(); got != 1 {
		t.Fatalf("rollback body ran %d times across two runs, want 1: the cache lookup did not reach the recovery node", got)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	rollback, err := st.GetNode(context.Background(), second.RunID, "rollback")
	if err != nil || rollback == nil {
		t.Fatalf("get rollback node: %v", err)
	}
	if rollback.Outcome != string(sparkwing.Cached) {
		t.Fatalf("rollback outcome = %q, want cached", rollback.Outcome)
	}
}

// TestOnFailure_RecoveryNodeHonorsSkipIf pins that a recovery node's
// SkipIf is evaluated. The local shortcut used to run the body without
// asking, so a rollback guarded by "only in staging" fired in
// production locally and was skipped in a pod.
func TestOnFailure_RecoveryNodeHonorsSkipIf(t *testing.T) {
	recoverySkipIfRuns.Store(0)
	p := newPaths(t)

	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "mod-onfailure-skipif"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := recoverySkipIfRuns.Load(); got != 0 {
		t.Fatalf("rollback body ran %d times, want 0: SkipIf was not evaluated", got)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	rollback, err := st.GetNode(context.Background(), res.RunID, "rollback")
	if err != nil || rollback == nil {
		t.Fatalf("get rollback node: %v", err)
	}
	if rollback.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("rollback outcome = %q, want skipped", rollback.Outcome)
	}
}

// TestOnFailure_RecoveryNodeTakesAConcurrencySlot pins the sharpest
// edge of the envelope change: a recovery node enrolled in a full
// group under OnLimit:Fail now fails there. It used to run, because
// the local shortcut never asked the group for a slot -- so a rollback
// declared as "at most one of these at a time" could run alongside the
// very work it was meant to be exclusive with.
func TestOnFailure_RecoveryNodeTakesAConcurrencySlot(t *testing.T) {
	recoveryConcurrencyRuns.Store(0)
	gate := installRecoveryHolderGate(t)
	p := newPaths(t)

	type runResult struct {
		res *orchestrator.Result
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		res, err := orchestrator.RunLocal(context.Background(), p,
			orchestrator.Options{Pipeline: "mod-onfailure-concurrency"})
		done <- runResult{res, err}
	}()

	select {
	case <-gate.acquired:
	case <-time.After(30 * time.Second):
		t.Fatal("hold never took the group's slot")
	}
	// The holder keeps the only slot until the recovery node has been
	// resolved against it, so the run cannot finish before the thing
	// under test happens.
	awaitTerminalNode(t, p, "rollback")
	gate.letGo()

	var got runResult
	select {
	case got = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("run never finished after the holder released")
	}
	if got.err != nil {
		t.Fatalf("Run: %v", got.err)
	}
	if n := recoveryConcurrencyRuns.Load(); n != 0 {
		t.Fatalf("rollback body ran %d times, want 0: it took a slot the group had already given away", n)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	rollback, err := st.GetNode(context.Background(), got.res.RunID, "rollback")
	if err != nil || rollback == nil {
		t.Fatalf("get rollback node: %v", err)
	}
	if rollback.Outcome != string(sparkwing.Failed) {
		t.Fatalf("rollback outcome = %q, want failed under OnLimit:Fail", rollback.Outcome)
	}
	if !strings.Contains(rollback.Error, "OnLimit:Fail") {
		t.Fatalf("rollback error = %q, want the at-capacity refusal", rollback.Error)
	}
}

// awaitTerminalNode blocks until nodeID of the newest run in the store
// carries an outcome, so a test can act on a mid-run fact without
// racing the dispatcher.
func awaitTerminalNode(t *testing.T, p orchestrator.Paths, nodeID string) {
	t.Helper()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open runs store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := st.ListRuns(ctx, store.RunFilter{Limit: 1})
		if err == nil && len(runs) == 1 {
			if n, err := st.GetNode(ctx, runs[0].ID, nodeID); err == nil && n != nil && n.Outcome != "" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node %q never reached a terminal outcome", nodeID)
}
