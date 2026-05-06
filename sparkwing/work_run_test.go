package sparkwing_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// recordingWorkLogger captures step events emitted by RunWork so
// tests can assert on the structured envelope (event, step, outcome,
// duration_ms).
type recordingWorkLogger struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (l *recordingWorkLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}
func (l *recordingWorkLogger) Emit(rec sparkwing.LogRecord) {
	l.mu.Lock()
	l.records = append(l.records, rec)
	l.mu.Unlock()
}
func (l *recordingWorkLogger) snapshot() []sparkwing.LogRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]sparkwing.LogRecord, len(l.records))
	copy(out, l.records)
	return out
}

func newWorkCtx() (context.Context, *recordingWorkLogger) {
	l := &recordingWorkLogger{}
	ctx := sparkwing.WithLogger(context.Background(), l)
	ctx = sparkwing.WithNode(ctx, "test-node")
	return ctx, l
}

func TestRunWork_SingleStepEmitsStartAndEnd(t *testing.T) {
	ctx, log := newWorkCtx()
	w := sparkwing.NewWork()
	sparkwing.Step(w, "only", func(ctx context.Context) error { return nil })

	out, err := sparkwing.RunWork(ctx, w)
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if out != nil {
		t.Fatalf("non-typed Work should return nil output, got %v", out)
	}

	recs := log.snapshot()
	var sawStart, sawEnd bool
	for _, r := range recs {
		if r.Event == "step_start" && r.Msg == "only" {
			sawStart = true
		}
		if r.Event == "step_end" && r.Msg == "only" {
			sawEnd = true
			if r.Attrs["outcome"] != "success" {
				t.Fatalf("step_end outcome = %v, want success", r.Attrs["outcome"])
			}
			if _, ok := r.Attrs["duration_ms"]; !ok {
				t.Fatalf("step_end missing duration_ms attr: %+v", r.Attrs)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing step boundary events: start=%v end=%v records=%+v", sawStart, sawEnd, recs)
	}
}

// TestRunWork_DependencyOrder verifies that a step with deps runs
// strictly after its upstream completes.
func TestRunWork_DependencyOrder(t *testing.T) {
	ctx, _ := newWorkCtx()
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(name string) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}

	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { record("a-end"); return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { record("b-end"); return nil }).Needs(a)

	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	_ = b
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != "a-end" || events[1] != "b-end" {
		t.Fatalf("dependency order violated: %v", events)
	}
}

// TestRunWork_ParallelStepsRunConcurrently verifies that two steps
// without a dependency run at the same time.
func TestRunWork_ParallelStepsRunConcurrently(t *testing.T) {
	ctx, _ := newWorkCtx()
	var inFlight, peak int32
	enter := func() {
		v := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&peak)
			if v <= cur || atomic.CompareAndSwapInt32(&peak, cur, v) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
	}

	w := sparkwing.NewWork()
	sparkwing.Step(w, "a", func(ctx context.Context) error { enter(); return nil })
	sparkwing.Step(w, "b", func(ctx context.Context) error { enter(); return nil })
	sparkwing.Step(w, "c", func(ctx context.Context) error { enter(); return nil })

	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if atomic.LoadInt32(&peak) < 2 {
		t.Fatalf("expected concurrency >= 2 across independent steps, peak=%d", peak)
	}
}

// TestRunWork_FailFastCancelsSiblings verifies that one step's
// failure cancels in-flight parallel siblings via the shared ctx.
func TestRunWork_FailFastCancelsSiblings(t *testing.T) {
	ctx, _ := newWorkCtx()
	var siblingObserved atomic.Bool
	siblingStarted := make(chan struct{})

	w := sparkwing.NewWork()
	sparkwing.Step(w, "sibling", func(ctx context.Context) error {
		close(siblingStarted)
		select {
		case <-ctx.Done():
			siblingObserved.Store(true)
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("sibling not cancelled")
		}
	})
	sparkwing.Step(w, "fails", func(ctx context.Context) error {
		<-siblingStarted
		return errors.New("nope")
	})

	out, err := sparkwing.RunWork(ctx, w)
	if err == nil {
		t.Fatal("expected error from fails step")
	}
	if out != nil {
		t.Fatalf("output should be nil on failure, got %v", out)
	}
	if !siblingObserved.Load() {
		t.Fatal("sibling did not observe ctx cancellation")
	}
}

// TestRunWork_TypedResultRecordsOnStep runs a multi-step Work whose
// terminal step is typed; RunWork itself returns nil for the value
// (the orchestrator reads typed output via node.ResultStep().Output()),
// but the typed step's MarkDone must persist the typed value so
// readers see it.
func TestRunWork_TypedResultRecordsOnStep(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	prep := sparkwing.Step(w, "prep", func(ctx context.Context) error { return nil })
	produce := sparkwing.Step(w, "produce", func(ctx context.Context) (fooOut, error) {
		return fooOut{Tag: "v9"}, nil
	})
	produce.Needs(prep)

	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	got, ok := produce.Output().(fooOut)
	if !ok {
		t.Fatalf("produce step output type = %T, want fooOut", produce.Output())
	}
	if got.Tag != "v9" {
		t.Fatalf("produce step Tag = %q, want v9", got.Tag)
	}
}

// TestRunWork_StepGetResolvesInDownstream confirms the in-process
// resolution path: a downstream step calling sw.StepGet[T](ctx, step)
// on its upstream gets the typed value back once MarkDone has fired.
// This is the canonical fixture for typed inter-step composition under
// the SDK-042 single-Step grammar.
func TestRunWork_StepGetResolvesInDownstream(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	tags := sparkwing.Step(w, "tags", func(ctx context.Context) (fooOut, error) {
		return fooOut{Tag: "abcd"}, nil
	})
	var seen string
	sparkwing.Step(w, "publish", func(ctx context.Context) error {
		seen = sparkwing.StepGet[fooOut](ctx, tags).Tag
		return nil
	}).Needs(tags)

	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if seen != "abcd" {
		t.Fatalf("downstream observed Tag = %q, want abcd", seen)
	}
}

// TestRunWork_SkipIfShortCircuits verifies a true SkipIf prevents
// the step's fn from running while still propagating to downstream.
func TestRunWork_SkipIfShortCircuits(t *testing.T) {
	ctx, log := newWorkCtx()
	var ran atomic.Bool

	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error {
		ran.Store(true)
		return nil
	}).SkipIf(func(ctx context.Context) bool { return true })
	sparkwing.Step(w, "b", func(ctx context.Context) error { return nil }).Needs(a)

	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if ran.Load() {
		t.Fatal("step a should not have executed when SkipIf returns true")
	}
	var skipped bool
	for _, r := range log.snapshot() {
		if r.Event == "step_skipped" && r.Msg == "a" {
			skipped = true
			if r.Attrs["outcome"] != "skipped" {
				t.Fatalf("step_skipped outcome = %v, want skipped", r.Attrs["outcome"])
			}
		}
	}
	if !skipped {
		t.Fatal("expected step_skipped event for step a")
	}
}

// TestRunWork_CycleProducesError ensures that a cyclic dep graph is
// detected (no zero-in-degree step exists).
func TestRunWork_CycleProducesError(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil })
	a.Needs(b)
	b.Needs(a)

	_, err := sparkwing.RunWork(ctx, w)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

// TestRunWork_PanicInStepBecomesError makes sure a panicking step
// fails the run cleanly instead of taking down the runner.
func TestRunWork_PanicInStepBecomesError(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	sparkwing.Step(w, "oops", func(ctx context.Context) error {
		panic("dropped a wrench")
	})
	_, err := sparkwing.RunWork(ctx, w)
	if err == nil {
		t.Fatal("expected error from panicking step")
	}
}

// TestRunWork_SpawnRejectedWithoutHandler verifies the loud-failure
// guard: Work that declares spawns errors out when no SpawnHandler
// is installed in ctx (rather than silently dropping the spawn).
func TestRunWork_SpawnRejectedWithoutHandler(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	sparkwing.JobSpawn(w, "scan", func(ctx context.Context) error { return nil })

	_, err := sparkwing.RunWork(ctx, w)
	if err == nil {
		t.Fatal("RunWork should reject spawn-bearing Work without a SpawnHandler in ctx")
	}
}

// TestRunWork_SpawnDispatchedThroughHandler installs a stub handler
// and verifies that runtime fan-out flows: SpawnNode triggers the
// handler with the right (parent, id, job) arguments, and the
// returned output is observable via SpawnHandle.
func TestRunWork_SpawnDispatchedThroughHandler(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	scan := sparkwing.JobSpawn(w, "scan", func(ctx context.Context) error { return nil }).Needs(a)
	var afterSawSpawn bool
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		// SpawnHandle.Spec().ResolvedID is set by the handler stub.
		if scan.Spec().ResolvedID() == "test-node/scan" {
			afterSawSpawn = true
		}
		return nil
	}).Needs(scan)

	calls := 0
	handler := sparkwing.SpawnHandlerFunc(func(ctx context.Context, parent, id string, job sparkwing.Workable) (any, error) {
		calls++
		if parent != "test-node" {
			t.Errorf("handler parent = %q, want test-node", parent)
		}
		if id != "scan" {
			t.Errorf("handler id = %q, want scan", id)
		}
		scan.Spec().SetResolvedID(parent + "/" + id)
		return nil, nil
	})

	hctx := sparkwing.WithSpawnHandler(ctx, handler)
	if _, err := sparkwing.RunWork(hctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler called %d times, want 1", calls)
	}
	if !afterSawSpawn {
		t.Fatal("downstream step did not see the resolved spawn id (sequencing broken)")
	}
}

// TestRunWork_SpawnForEachDispatchesEachItem verifies that
// SpawnNodeForEach calls the handler once per slice element with the
// per-item id + job the closure produces.
func TestRunWork_SpawnForEachDispatchesEachItem(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	items := []string{"alpha", "beta", "gamma"}
	sparkwing.JobSpawnEach(w, items, func(s string) (string, any) {
		return "shard-" + s, func(ctx context.Context) error { return nil }
	})

	var seen sync.Map
	handler := sparkwing.SpawnHandlerFunc(func(ctx context.Context, parent, id string, job sparkwing.Workable) (any, error) {
		seen.Store(id, true)
		return nil, nil
	})

	hctx := sparkwing.WithSpawnHandler(ctx, handler)
	if _, err := sparkwing.RunWork(hctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	for _, item := range items {
		if _, ok := seen.Load("shard-" + item); !ok {
			t.Errorf("handler missed item %q", item)
		}
	}
}
