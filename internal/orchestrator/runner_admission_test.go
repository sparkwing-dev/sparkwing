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
	release <-chan struct{}
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
	case <-r.release:
		return runner.Result{Outcome: sparkwing.Success}
	case <-ctx.Done():
		return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}
}

func TestRunnerAdmissionAppliesToNonInProcessRunner(t *testing.T) {
	register(runnerAdmissionPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return runnerAdmissionPipe{}
	})

	release := make(chan struct{})
	defer close(release)
	fake := &blockingRunner{
		entered:  make(chan string, runnerAdmissionNodes),
		release:  release,
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
}
