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
