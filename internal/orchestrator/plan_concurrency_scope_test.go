package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// planScopePipe declares a whole-plan concurrency group built by its
// factory, so one pipeline shape exercises any scope/limit combination.
type planScopePipe struct {
	sparkwing.Base
	group *sparkwing.ConcurrencyGroup
}

func (p *planScopePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Concurrency(p.group)
	sparkwing.Job(plan, "work", func(ctx context.Context) error { return nil })
	return nil
}

// planAcquireCapture records every AcquireSlot key and answers with a
// fixed kind; ResolveWaiter parks queue-kind waiters forever, naming a
// fixed holder.
type planAcquireCapture struct {
	fakeConcurrency
	mu   sync.Mutex
	kind store.AcquireKind
	keys []string
}

func (c *planAcquireCapture) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	c.mu.Lock()
	c.keys = append(c.keys, req.Key)
	c.mu.Unlock()
	return store.AcquireSlotResponse{Kind: c.kind, HolderID: req.HolderID, Position: 1}, nil
}

func (c *planAcquireCapture) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	return store.WaiterResolution{
		Status:  store.WaiterStillWaiting,
		Holders: []store.ConcurrencyHolder{{RunID: "run-20260701-000000-b10cc4e1"}},
	}, nil
}

func (c *planAcquireCapture) capturedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

func runPlanScopePipe(t *testing.T, ctx context.Context, name string, group *sparkwing.ConcurrencyGroup, conc *planAcquireCapture) *orchestrator.Result {
	t.Helper()
	register(name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return &planScopePipe{group: group} })
	fakes := newFakeBackends()
	res, err := orchestrator.Run(ctx,
		orchestrator.Backends{State: fakes.state, Logs: fakes.logs, Concurrency: conc},
		orchestrator.Options{Pipeline: name})
	if err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	return res
}

func TestPlanConcurrency_KeyCarriesScope(t *testing.T) {
	t.Setenv("SPARKWING_BOX_ID", "testbox")
	global := sparkwing.NewConcurrencyGroup("deploy-scope-x", sparkwing.ConcurrencyLimit{Capacity: 1})
	boxed := sparkwing.NewConcurrencyGroup("deploy-scope-x", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox})
	concGlobal := &planAcquireCapture{kind: store.AcquireGranted}
	concBox := &planAcquireCapture{kind: store.AcquireGranted}

	runPlanScopePipe(t, context.Background(), "plan-scope-global", global, concGlobal)
	runPlanScopePipe(t, context.Background(), "plan-scope-box", boxed, concBox)

	globalKeys, boxKeys := concGlobal.capturedKeys(), concBox.capturedKeys()
	if len(globalKeys) == 0 || len(boxKeys) == 0 {
		t.Fatalf("acquires not captured: global=%v box=%v", globalKeys, boxKeys)
	}
	if want := "g:deploy-scope-x"; globalKeys[0] != want {
		t.Errorf("global plan key = %q, want %q (node-level scheme)", globalKeys[0], want)
	}
	if want := "b:7:testboxdeploy-scope-x"; boxKeys[0] != want {
		t.Errorf("box plan key = %q, want %q (node-level scheme)", boxKeys[0], want)
	}
	if globalKeys[0] == boxKeys[0] {
		t.Fatalf("same-name groups with different scopes alias onto key %q", globalKeys[0])
	}
}

func TestPlanConcurrency_QueueTimeoutTripsLoud(t *testing.T) {
	group := sparkwing.NewConcurrencyGroup("deploy-qt-trip", sparkwing.ConcurrencyLimit{
		Capacity:     1,
		OnLimit:      sparkwing.Queue,
		QueueTimeout: 300 * time.Millisecond,
	})
	conc := &planAcquireCapture{kind: store.AcquireQueued}

	res := runPlanScopePipe(t, context.Background(), "plan-scope-qt-trip", group, conc)

	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed on queue timeout", res.Status)
	}
	for _, want := range []string{"deploy-qt-trip", "300ms", "run-20260701-000000-b10cc4e1"} {
		if !strings.Contains(res.Error.Error(), want) {
			t.Errorf("error %q missing %q", res.Error, want)
		}
	}
}

func TestPlanConcurrency_ZeroQueueTimeoutWaitsIndefinitely(t *testing.T) {
	group := sparkwing.NewConcurrencyGroup("deploy-qt-zero", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		OnLimit:  sparkwing.Queue,
	})
	conc := &planAcquireCapture{kind: store.AcquireQueued}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	res := runPlanScopePipe(t, ctx, "plan-scope-qt-zero", group, conc)

	if res.Status != "failed" || res.Error == nil {
		t.Fatalf("res = %+v, want context-bounded failure", res)
	}
	if strings.Contains(res.Error.Error(), "without a slot") {
		t.Fatalf("zero QueueTimeout tripped the queue bound: %v (must wait until the context ends)", res.Error)
	}
	if !strings.Contains(res.Error.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("error %q is not the context deadline; the wait must have ended only with the context", res.Error)
	}
}
