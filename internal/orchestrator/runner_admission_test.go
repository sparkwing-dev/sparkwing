package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	runnerAdmissionPipeline = "runner-admission"
	runnerAdmissionNodes    = 8
	runnerAdmissionCapacity = 4
)

type runnerAdmissionPipe struct{ sparkwing.Base }

func (runnerAdmissionPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("runner-admission", sparkwing.ConcurrencyLimit{
		Capacity: runnerAdmissionCapacity,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Queue,
	})
	for ordinal := range runnerAdmissionNodes {
		sparkwing.Job(plan, fmt.Sprintf("shard-%d", ordinal+1), func(context.Context) error {
			return nil
		}).Concurrency(group)
	}
	return nil
}

type blockingRunner struct {
	entered chan string
	permits <-chan struct{}
	active  atomic.Int32
	peak    atomic.Int32

	mu       sync.Mutex
	attempts map[string]int
}

func (r *blockingRunner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	r.mu.Lock()
	r.attempts[req.NodeID]++
	r.mu.Unlock()

	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		peak := r.peak.Load()
		if active <= peak || r.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	r.entered <- req.NodeID

	select {
	case <-r.permits:
		return runner.Result{Outcome: sparkwing.Success}
	case <-ctx.Done():
		return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}
}

func TestRunnerAdmissionAppliesToNonInProcessRunner(t *testing.T) {
	register(runnerAdmissionPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return runnerAdmissionPipe{}
	})

	permits := make(chan struct{}, runnerAdmissionNodes)
	fake := &blockingRunner{
		entered:  make(chan string, runnerAdmissionNodes),
		permits:  permits,
		attempts: make(map[string]int),
	}

	done := make(chan *orchestrator.Result, 1)
	paths := newPaths(t)
	go func() {
		result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
			Pipeline:    runnerAdmissionPipeline,
			Runner:      fake,
			MaxParallel: runnerAdmissionNodes,
		})
		if err != nil {
			done <- &orchestrator.Result{Status: "setup-failed", Error: err}
			return
		}
		done <- result
	}()

	for range runnerAdmissionCapacity {
		select {
		case <-fake.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("four admitted runner calls did not start")
		}
	}

	select {
	case nodeID := <-fake.entered:
		t.Fatalf("runner call %s entered before a concurrency slot was released", nodeID)
	case <-time.After(100 * time.Millisecond):
	}

	permits <- struct{}{}
	select {
	case <-fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fifth runner call did not enter after a concurrency slot was released")
	}
	for range runnerAdmissionNodes - 1 {
		permits <- struct{}{}
	}

	select {
	case result := <-done:
		if result.Status != "success" || result.Error != nil {
			t.Fatalf("run status = %q, error = %v", result.Status, result.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("coordinated run did not finish")
	}
	if got := fake.peak.Load(); got != runnerAdmissionCapacity {
		t.Fatalf("peak runner calls = %d, want exactly %d", got, runnerAdmissionCapacity)
	}
	if got := fake.active.Load(); got != 0 {
		t.Fatalf("active runner calls after completion = %d, want 0", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.attempts) != runnerAdmissionNodes {
		t.Fatalf("runner attempts cover %d nodes, want %d", len(fake.attempts), runnerAdmissionNodes)
	}
	for nodeID, attempts := range fake.attempts {
		if attempts != 1 {
			t.Errorf("runner attempts for %s = %d, want 1", nodeID, attempts)
		}
	}
}
