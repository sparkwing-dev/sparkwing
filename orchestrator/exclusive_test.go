package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// --- Exclusive ---

// exclusiveCounter tracks the number of in-flight nodes holding the
// Exclusive lock; test code asserts it never exceeds 1.
type exclusiveCounter struct {
	inflight int32
	maxSeen  int32
}

func (e *exclusiveCounter) step(holdFor time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := atomic.AddInt32(&e.inflight, 1)
		defer atomic.AddInt32(&e.inflight, -1)
		// Track the peak concurrency we observe while the lock is held.
		for {
			peak := atomic.LoadInt32(&e.maxSeen)
			if cur <= peak || atomic.CompareAndSwapInt32(&e.maxSeen, peak, cur) {
				break
			}
		}
		time.Sleep(holdFor)
		return nil
	}
}

type exclusivePipe struct{ sparkwing.Base }

var exclusiveState = &exclusiveCounter{}

func (exclusivePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {	// Two peer nodes, both exclusive on the same key, both try to
	// run concurrently. The lock should serialize them.
	sparkwing.Job(plan, "a", sparkwing.JobFn(exclusiveState.step(150*time.Millisecond))).Cache(sparkwing.CacheOptions{Key: "shared-resource"})
	sparkwing.Job(plan, "b", sparkwing.JobFn(exclusiveState.step(150*time.Millisecond))).Cache(sparkwing.CacheOptions{Key: "shared-resource"})
	return nil
}

// --- NeedsOptional ---

type optionalDepsPipe struct{ sparkwing.Base }

var (
	optA atomic.Bool
	optB atomic.Bool
)

func (optionalDepsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {	a := sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error {
		optA.Store(true)
		return nil
	}))
	// b declares NeedsOptional("a", "missing-node"). Should wait on
	// a (present) and silently skip the missing ID.
	sparkwing.Job(plan, "b", sparkwing.JobFn(func(ctx context.Context) error {
		if !optA.Load() {
			return errors.New("b ran before a")
		}
		optB.Store(true)
		return nil
	})).NeedsOptional(a, "missing-node")
	return nil
}

// --- ContinueOnError ---

type continueOnErrorPipe struct{ sparkwing.Base }

var (
	cOErrFailRan atomic.Bool
	cOErrNextRan atomic.Bool
)

func (continueOnErrorPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {	failer := sparkwing.Job(plan, "failer", sparkwing.JobFn(func(ctx context.Context) error {
		cOErrFailRan.Store(true)
		return errors.New("planned failure")
	})).ContinueOnError()
	sparkwing.Job(plan, "next", sparkwing.JobFn(func(ctx context.Context) error {
		cOErrNextRan.Store(true)
		return nil
	})).Needs(failer)
	return nil
}

// --- Optional ---

type optionalFailurePipe struct{ sparkwing.Base }

var (
	optFailRan  atomic.Bool
	optFailNext atomic.Bool
)

func (optionalFailurePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {	bad := sparkwing.Job(plan, "bad", sparkwing.JobFn(func(ctx context.Context) error {
		optFailRan.Store(true)
		return errors.New("optional failure")
	})).Optional()
	sparkwing.Job(plan, "after", sparkwing.JobFn(func(ctx context.Context) error {
		optFailNext.Store(true)
		return nil
	})).Needs(bad)
	return nil
}

func init() {
	register("exclusive-serialize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &exclusivePipe{} })
	register("needs-optional", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &optionalDepsPipe{} })
	register("continue-on-error", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &continueOnErrorPipe{} })
	register("optional-failure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &optionalFailurePipe{} })
}

// --- Tests ---

func TestExclusive_SerializesConcurrentHolders(t *testing.T) {
	atomic.StoreInt32(&exclusiveState.inflight, 0)
	atomic.StoreInt32(&exclusiveState.maxSeen, 0)

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "exclusive-serialize"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}

	peak := atomic.LoadInt32(&exclusiveState.maxSeen)
	if peak > 1 {
		t.Fatalf("Exclusive peak concurrency = %d, want 1", peak)
	}
}

func TestExclusive_AcrossRuns(t *testing.T) {
	// Two separate orchestrator.Run calls with Exclusive("k") should
	// still serialize via the file lock.
	atomic.StoreInt32(&exclusiveState.inflight, 0)
	atomic.StoreInt32(&exclusiveState.maxSeen, 0)

	p := newPaths(t)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "exclusive-serialize"})
		}()
	}
	wg.Wait()

	peak := atomic.LoadInt32(&exclusiveState.maxSeen)
	if peak > 1 {
		t.Fatalf("Exclusive peak concurrency across runs = %d, want 1", peak)
	}
}

func TestNeedsOptional_WaitsForPresent(t *testing.T) {
	optA.Store(false)
	optB.Store(false)
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "needs-optional"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !optA.Load() || !optB.Load() {
		t.Fatal("both a and b should have run")
	}
}

func TestContinueOnError_DownstreamProceeds(t *testing.T) {
	cOErrFailRan.Store(false)
	cOErrNextRan.Store(false)

	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "continue-on-error"})

	if !cOErrFailRan.Load() {
		t.Fatal("failer should have run")
	}
	if !cOErrNextRan.Load() {
		t.Fatal("next should have run despite failer's failure")
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (ContinueOnError only unblocks dispatch, not run outcome)", res.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["failer"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("failer outcome = %q", byID["failer"].Outcome)
	}
	if byID["next"].Outcome != string(sparkwing.Success) {
		t.Fatalf("next outcome = %q", byID["next"].Outcome)
	}
}

func TestOptional_FailureDoesNotFailRun(t *testing.T) {
	optFailRan.Store(false)
	optFailNext.Store(false)
	p := newPaths(t)
	res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "optional-failure"})

	if !optFailRan.Load() {
		t.Fatal("bad should have run")
	}
	if !optFailNext.Load() {
		t.Fatal("after should have run (Optional implies ContinueOnError)")
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (Optional failure doesn't propagate)", res.Status)
	}
}
