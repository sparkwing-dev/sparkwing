package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type exclusiveCounter struct {
	inflight    int32
	maxSeen     int32
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (e *exclusiveCounter) reset(entryCapacity int) {
	atomic.StoreInt32(&e.inflight, 0)
	atomic.StoreInt32(&e.maxSeen, 0)
	e.entered = make(chan struct{}, entryCapacity)
	e.release = make(chan struct{})
	e.releaseOnce = sync.Once{}
}

func (e *exclusiveCounter) releaseAll() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *exclusiveCounter) step() func(ctx context.Context) error {
	return func(ctx context.Context) error {
		cur := atomic.AddInt32(&e.inflight, 1)
		defer atomic.AddInt32(&e.inflight, -1)
		for {
			peak := atomic.LoadInt32(&e.maxSeen)
			if cur <= peak || atomic.CompareAndSwapInt32(&e.maxSeen, peak, cur) {
				break
			}
		}
		e.entered <- struct{}{}
		select {
		case <-e.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type exclusivePipe struct{ sparkwing.Base }

var exclusiveState = &exclusiveCounter{}

func (exclusivePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("shared-resource", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "a", exclusiveState.step()).Concurrency(g)
	sparkwing.Job(plan, "b", exclusiveState.step()).Concurrency(g)
	return nil
}

type optionalDepsPipe struct{ sparkwing.Base }

var (
	optA atomic.Bool
	optB atomic.Bool
)

func (optionalDepsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", func(ctx context.Context) error {
		optA.Store(true)
		return nil
	})
	sparkwing.Job(plan, "b", func(ctx context.Context) error {
		if !optA.Load() {
			return errors.New("b ran before a")
		}
		optB.Store(true)
		return nil
	}).NeedsOptional(a)
	return nil
}

type continueOnErrorPipe struct{ sparkwing.Base }

var (
	cOErrFailRan atomic.Bool
	cOErrNextRan atomic.Bool
)

func (continueOnErrorPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	failer := sparkwing.Job(plan, "failer", func(ctx context.Context) error {
		cOErrFailRan.Store(true)
		return errors.New("planned failure")
	}).ContinueOnError()
	sparkwing.Job(plan, "next", func(ctx context.Context) error {
		cOErrNextRan.Store(true)
		return nil
	}).Needs(failer)
	return nil
}

type optionalFailurePipe struct{ sparkwing.Base }

var (
	optFailRan  atomic.Bool
	optFailNext atomic.Bool
)

func (optionalFailurePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	bad := sparkwing.Job(plan, "bad", func(ctx context.Context) error {
		optFailRan.Store(true)
		return errors.New("optional failure")
	}).Optional()
	sparkwing.Job(plan, "after", func(ctx context.Context) error {
		optFailNext.Store(true)
		return nil
	}).Needs(bad)
	return nil
}

func init() {
	register("exclusive-serialize", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &exclusivePipe{} })
	register("needs-optional", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &optionalDepsPipe{} })
	register("continue-on-error", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &continueOnErrorPipe{} })
	register("optional-failure", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &optionalFailurePipe{} })
}

func TestExclusive_SerializesConcurrentHolders(t *testing.T) {
	p := newPaths(t)
	assertExclusiveSerialization(t, p, 1)
}

func TestExclusive_AcrossRuns(t *testing.T) {
	p := newPaths(t)
	assertExclusiveSerialization(t, p, 2)
}

func assertExclusiveSerialization(t *testing.T, p orchestrator.Paths, runs int) {
	t.Helper()
	exclusiveState.reset(runs * 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	results := make(chan error, runs)
	var wg sync.WaitGroup
	for range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := orchestrator.RunLocal(ctx, p, orchestrator.Options{Pipeline: "exclusive-serialize"})
			results <- err
		}()
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		exclusiveState.releaseAll()
		cancel()
		join := time.NewTimer(time.Second)
		defer join.Stop()
		select {
		case <-finished:
		case <-join.C:
			t.Error("exclusive runs did not stop during cleanup")
		}
	})

	first := time.NewTimer(3 * time.Second)
	defer first.Stop()
	select {
	case <-exclusiveState.entered:
	case <-first.C:
		t.Fatal("no exclusive holder entered")
	}

	waitForExclusivePopulation(t, ctx, st, runs*2-1)
	select {
	case <-exclusiveState.entered:
		t.Fatal("a second exclusive holder entered before release")
	default:
	}
	exclusiveState.releaseAll()

	join := time.NewTimer(5 * time.Second)
	defer join.Stop()
	select {
	case <-finished:
	case <-join.C:
		t.Fatal("exclusive runs did not finish after release")
	}
	for range runs {
		if err := <-results; err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	peak := atomic.LoadInt32(&exclusiveState.maxSeen)
	if peak > 1 {
		t.Fatalf("Exclusive peak concurrency across runs = %d, want 1", peak)
	}
}

func waitForExclusivePopulation(t *testing.T, ctx context.Context, st *store.Store, wantWaiters int) {
	t.Helper()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		state, err := st.GetConcurrencyState(ctx, "g:shared-resource")
		switch {
		case err == nil && len(state.Holders) == 1 && len(state.Waiters) == wantWaiters:
			return
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
		default:
			t.Fatalf("read exclusive concurrency state: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("exclusive concurrency population did not reach 1 holder and %d waiters", wantWaiters)
		case <-poll.C:
		}
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
	defer func() { _ = st.Close() }()
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
