package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type wedgePipe struct{ sparkwing.Base }

func (wedgePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "gated", func(ctx context.Context) error { return nil }).
		Concurrency(sparkwing.NewConcurrencyGroup("wedge-lock", sparkwing.ConcurrencyLimit{Capacity: 1}))
	return nil
}

// wedgeConcurrency queues every arrival and errors ResolveWaiter for
// the first failResolves polls (forever when negative), then promotes.
type wedgeConcurrency struct {
	fakeConcurrency
	mu           sync.Mutex
	failResolves int
	resolveErr   error
	resolves     int
}

func (w *wedgeConcurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	return store.AcquireSlotResponse{Kind: store.AcquireQueued, Position: 1, QueueLength: 1}, nil
}

func (w *wedgeConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resolves++
	if w.failResolves < 0 || w.resolves <= w.failResolves {
		return store.WaiterResolution{}, w.resolveErr
	}
	return store.WaiterResolution{Status: store.WaiterPromoted, HolderID: runID + "/" + nodeID}, nil
}

// nodeErrRecordingState wraps fakeState to capture each node's
// terminal errMsg, since Result.Error only lists failed node ids.
type nodeErrRecordingState struct {
	*fakeState
	mu       sync.Mutex
	nodeErrs map[string]string
}

func (r *nodeErrRecordingState) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	r.mu.Lock()
	r.nodeErrs[nodeID] = errMsg
	r.mu.Unlock()
	return r.fakeState.FinishNode(ctx, runID, nodeID, outcome, errMsg, output)
}

func (r *nodeErrRecordingState) nodeErr(nodeID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeErrs[nodeID]
}

func runWedgePipe(t *testing.T, conc *wedgeConcurrency) (*orchestrator.Result, *nodeErrRecordingState) {
	t.Helper()
	register("wedge-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &wedgePipe{} })
	fakes := newFakeBackends()
	state := &nodeErrRecordingState{fakeState: fakes.state, nodeErrs: map[string]string{}}
	res, err := orchestrator.Run(context.Background(),
		orchestrator.Backends{State: state, Logs: fakes.logs, Concurrency: conc},
		orchestrator.Options{Pipeline: "wedge-pipe"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, state
}

func TestWaitThenRun_ContinuousResolveFailureTripsWedgeBudget(t *testing.T) {
	t.Setenv(orchestrator.StoreWedgeBudgetEnvVar, "250ms")
	conc := &wedgeConcurrency{failResolves: -1, resolveErr: errors.New("database is locked (5) (SQLITE_BUSY)")}

	res, state := runWedgePipe(t, conc)

	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	for _, want := range []string{"wedge-lock", "database is locked", "box-slots list", "budget 250ms"} {
		if !strings.Contains(state.nodeErr("gated"), want) {
			t.Errorf("node error %q missing %q", state.nodeErr("gated"), want)
		}
	}
}

func TestWaitThenRun_IntermittentResolveFailureRecovers(t *testing.T) {
	conc := &wedgeConcurrency{failResolves: 3, resolveErr: errors.New("database is locked (5) (SQLITE_BUSY)")}

	res, _ := runWedgePipe(t, conc)

	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); transient resolve errors must not fail the node", res.Status, res.Error)
	}
}

func TestWaitThenRun_LockingProtocolIsImmediatelyTerminal(t *testing.T) {
	conc := &wedgeConcurrency{failResolves: -1, resolveErr: errors.New("locking protocol (15) (SQLITE_PROTOCOL)")}

	start := time.Now()
	res, state := runWedgePipe(t, conc)

	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	for _, want := range []string{"locking protocol", "box-slots list"} {
		if !strings.Contains(state.nodeErr("gated"), want) {
			t.Errorf("node error %q missing %q", state.nodeErr("gated"), want)
		}
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("protocol failure took %s; want immediate, not a budget wait", elapsed)
	}
}

func TestAcquireAndRun_InvalidWedgeBudgetEnvFailsLoudly(t *testing.T) {
	t.Setenv(orchestrator.StoreWedgeBudgetEnvVar, "sometime")
	conc := &wedgeConcurrency{failResolves: 0}

	res, state := runWedgePipe(t, conc)

	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed on invalid %s", res.Status, orchestrator.StoreWedgeBudgetEnvVar)
	}
	if !strings.Contains(state.nodeErr("gated"), orchestrator.StoreWedgeBudgetEnvVar) {
		t.Errorf("node error %q does not name %s", state.nodeErr("gated"), orchestrator.StoreWedgeBudgetEnvVar)
	}
}
