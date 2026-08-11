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
	entered   chan string
	permits   <-chan struct{}
	immediate map[string]bool
	output    any
	active    atomic.Int32
	peak      atomic.Int32

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
	if r.immediate[req.NodeID] {
		return runner.Result{Outcome: sparkwing.Success, Output: r.output}
	}

	select {
	case <-r.permits:
		return runner.Result{Outcome: sparkwing.Success, Output: r.output}
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

type workerSlotReleasePipe struct{ sparkwing.Base }

func (workerSlotReleasePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("worker-slot-release", sparkwing.ConcurrencyLimit{
		Capacity: runnerAdmissionCapacity,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Queue,
	})
	for ordinal := range runnerAdmissionCapacity + 1 {
		sparkwing.Job(plan, fmt.Sprintf("group-%d", ordinal+1), func(context.Context) error {
			return nil
		}).Concurrency(group)
	}
	sparkwing.Job(plan, "unrelated", func(context.Context) error { return nil })
	return nil
}

func TestQueuedGroupNodeReleasesWorkerSlotForUnrelatedWork(t *testing.T) {
	const pipeline = "runner-worker-slot-release"
	register(pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return workerSlotReleasePipe{} })

	permits := make(chan struct{}, runnerAdmissionCapacity+2)
	fake := &blockingRunner{
		entered:   make(chan string, runnerAdmissionCapacity+2),
		permits:   permits,
		immediate: map[string]bool{"unrelated": true},
		attempts:  make(map[string]int),
	}
	paths := newPaths(t)
	done := make(chan *orchestrator.Result, 1)
	go func() {
		result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
			Pipeline:    pipeline,
			Runner:      fake,
			MaxParallel: runnerAdmissionCapacity + 1,
		})
		if err != nil {
			done <- &orchestrator.Result{Status: "setup-failed", Error: err}
			return
		}
		done <- result
	}()

	held := make(map[string]bool, runnerAdmissionCapacity)
	for range runnerAdmissionCapacity {
		select {
		case got := <-fake.entered:
			if got == "unrelated" || held[got] {
				t.Fatalf("initial group admission = %q, held = %v", got, held)
			}
			held[got] = true
		case <-time.After(5 * time.Second):
			t.Fatal("four group holders did not enter")
		}
	}
	select {
	case got := <-fake.entered:
		if got != "unrelated" {
			t.Fatalf("node entering released worker slot = %q, want unrelated", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued group node did not release its worker slot for unrelated work")
	}

	permits <- struct{}{}
	select {
	case got := <-fake.entered:
		if got == "unrelated" || held[got] {
			t.Fatalf("node promoted after group release = %q, held = %v", got, held)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued group node did not reacquire a worker slot after promotion")
	}
	for range runnerAdmissionCapacity {
		permits <- struct{}{}
	}
	select {
	case result := <-done:
		if result.Status != "success" || result.Error != nil {
			t.Fatalf("run status = %q, error = %v", result.Status, result.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker-slot release run did not finish")
	}
}

type customRunnerMemoGroupPipe struct{ sparkwing.Base }

func (customRunnerMemoGroupPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	group := sparkwing.NewConcurrencyGroup("custom-runner-memo", sparkwing.ConcurrencyLimit{
		Capacity: 1,
		Scope:    sparkwing.ScopeBox,
		OnLimit:  sparkwing.Queue,
	})
	cacheKey := func(context.Context) sparkwing.CacheKey { return sparkwing.Key("custom-runner", "shared") }
	for _, id := range []string{"leader", "follower"} {
		sparkwing.Job(plan, id, func(context.Context) error { return nil }).
			Concurrency(group).
			Cache(cacheKey)
	}
	return nil
}

func TestCustomRunnerCacheAndGroupInvokeDownstreamOnce(t *testing.T) {
	const pipeline = "custom-runner-cache-group"
	register(pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return customRunnerMemoGroupPipe{} })
	permits := make(chan struct{}, 1)
	fake := &blockingRunner{
		entered:  make(chan string, 2),
		permits:  permits,
		output:   map[string]string{"proof": "preserved"},
		attempts: make(map[string]int),
	}
	paths := newPaths(t)
	done := make(chan *orchestrator.Result, 1)
	go func() {
		result, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
			Pipeline:    pipeline,
			Runner:      fake,
			MaxParallel: 2,
		})
		if err != nil {
			done <- &orchestrator.Result{Status: "setup-failed", Error: err}
			return
		}
		done <- result
	}()
	select {
	case <-fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("memo leader did not reach the custom runner")
	}
	select {
	case got := <-fake.entered:
		t.Fatalf("memo follower reached downstream runner as %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	permits <- struct{}{}

	var result *orchestrator.Result
	select {
	case result = <-done:
		if result.Status != "success" || result.Error != nil {
			t.Fatalf("run status = %q, error = %v", result.Status, result.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("memoized custom-runner run did not finish")
	}
	fake.mu.Lock()
	if len(fake.attempts) != 1 {
		t.Fatalf("downstream runner attempts = %v, want exactly one node", fake.attempts)
	}
	fake.mu.Unlock()
	leader := nodeByID(t, paths, result.RunID, "leader")
	follower := nodeByID(t, paths, result.RunID, "follower")
	outcomes := map[string]bool{
		leader.Outcome:   true,
		follower.Outcome: true,
	}
	if !outcomes[string(sparkwing.Success)] || !outcomes[string(sparkwing.Cached)] {
		t.Fatalf("node outcomes = %v, want one success and one cached", outcomes)
	}
	if string(leader.Output) != `{"proof":"preserved"}` || string(follower.Output) != string(leader.Output) {
		t.Fatalf("node outputs = leader %s, follower %s; want preserved sentinel", leader.Output, follower.Output)
	}
}
