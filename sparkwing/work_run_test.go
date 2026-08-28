package sparkwing_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	ctx := sparkwingruntime.WithLogger(context.Background(), l)
	ctx = sparkwingruntime.WithNode(ctx, "test-node")
	return ctx, l
}

func TestRunWork_ExplicitFailFastCancelsSiblingAndRunsFinally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := sparkwing.NewWork().ParallelFailures(sparkwing.FailFast)
		started := make(chan struct{})
		slow := sparkwing.Step(w, "slow", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		failed := sparkwing.Step(w, "reject", func(context.Context) error {
			<-started
			return errors.New("rejected")
		})
		var pendingRan atomic.Bool
		pending := sparkwing.Step(w, "pending", func(context.Context) error {
			pendingRan.Store(true)
			return nil
		}).Needs(slow)
		var cleaned atomic.Bool
		sparkwing.Step(w, "cleanup", func(context.Context) error {
			cleaned.Store(true)
			return nil
		}).Needs(slow, failed, pending).Finally()

		ctx, logs := newWorkCtx()
		_, err := sparkwing.RunWork(ctx, w)
		if err == nil {
			t.Fatal("RunWork should fail")
		}
		if !cleaned.Load() {
			t.Fatal("finally cleanup did not run")
		}
		if pendingRan.Load() {
			t.Fatal("pending sibling ran after fail-fast")
		}

		cancelled := map[string]bool{}
		var summary bool
		failureIndex := -1
		firstCancellationIndex := -1
		for i, rec := range logs.snapshot() {
			if rec.Event == "step_end" && rec.Msg == "reject" && rec.Attrs["outcome"] == "failed" {
				failureIndex = i
			}
			if rec.Event == "step_end" && (rec.Msg == "slow" || rec.Msg == "pending") && rec.Attrs["outcome"] == "cancelled" {
				cancelled[rec.Msg] = true
				if firstCancellationIndex == -1 {
					firstCancellationIndex = i
				}
			}
			if rec.Event == "work_fail_fast" && rec.Attrs["trigger_step"] == "reject" {
				summary = true
				if got := fmt.Sprint(rec.Attrs["cancelled_steps"]); got != "[pending slow]" {
					t.Fatalf("cancelled_steps = %s, want [pending slow]", got)
				}
			}
		}
		if !cancelled["slow"] || !cancelled["pending"] {
			t.Fatalf("cancelled sibling telemetry = %v", cancelled)
		}
		if !summary {
			t.Fatal("fail-fast telemetry did not name the triggering step")
		}
		if failureIndex == -1 || firstCancellationIndex == -1 || failureIndex > firstCancellationIndex {
			t.Fatalf("terminal event order: failure=%d first cancellation=%d", failureIndex, firstCancellationIndex)
		}
	})
}

func TestRunWork_ParentCancellationDoesNotEmitFailFastTrigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := sparkwing.NewWork().ParallelFailures(sparkwing.FailFast)
		started := make(chan string, 2)
		for _, id := range []string{"first", "second"} {
			sparkwing.Step(w, id, func(ctx context.Context) error {
				started <- id
				<-ctx.Done()
				return ctx.Err()
			})
		}

		base, logs := newWorkCtx()
		ctx, cancel := context.WithCancel(base)
		done := make(chan error, 1)
		go func() {
			_, err := sparkwing.RunWork(ctx, w)
			done <- err
		}()

		seen := map[string]bool{}
		for len(seen) < 2 {
			seen[<-started] = true
		}
		cancel()

		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWork error = %v, want context.Canceled", err)
		}
		var stepErr *sparkwing.StepError
		if errors.As(err, &stepErr) {
			t.Fatalf("parent cancellation attributed to step %q", stepErr.StepID)
		}

		cancelled := map[string]bool{}
		for _, rec := range logs.snapshot() {
			if rec.Event == "step_end" && rec.Attrs["outcome"] == "cancelled" {
				cancelled[rec.Msg] = true
			}
			if rec.Event == sparkwing.EventWorkFailFast {
				t.Fatalf("parent cancellation emitted fail-fast trigger: %+v", rec)
			}
		}
		if !cancelled["first"] || !cancelled["second"] {
			t.Fatalf("cancelled step telemetry = %v, want both parallel steps", cancelled)
		}
	})
}

func TestRunWork_CollectAllFinishesParallelSiblings(t *testing.T) {
	w := sparkwing.NewWork().ParallelFailures(sparkwing.CollectAll)
	independentStarted := make(chan struct{})
	prerequisiteFailed := make(chan struct{})
	var independentCompleted atomic.Bool
	reject := sparkwing.Step(w, "reject", func(context.Context) error {
		<-independentStarted
		close(prerequisiteFailed)
		return errors.New("rejected")
	})
	sparkwing.Step(w, "independent", func(context.Context) error {
		close(independentStarted)
		<-prerequisiteFailed
		independentCompleted.Store(true)
		return nil
	})
	var dependentRan atomic.Bool
	sparkwing.Step(w, "dependent", func(context.Context) error {
		dependentRan.Store(true)
		return nil
	}).Needs(reject)

	_, err := sparkwing.RunWork(context.Background(), w)
	if err == nil {
		t.Fatal("CollectAll should preserve the failed rollup")
	}
	if !independentCompleted.Load() {
		t.Fatal("CollectAll cancelled a sibling")
	}
	if dependentRan.Load() {
		t.Fatal("CollectAll satisfied a hard Needs edge after its prerequisite failed")
	}
}

func TestRunWork_CollectAllAllowsDependentAfterContinueOnError(t *testing.T) {
	w := sparkwing.NewWork().ParallelFailures(sparkwing.CollectAll)
	reject := sparkwing.Step(w, "reject", func(context.Context) error {
		return errors.New("rejected")
	}).ContinueOnError()
	var dependentRan atomic.Bool
	sparkwing.Step(w, "dependent", func(context.Context) error {
		dependentRan.Store(true)
		return nil
	}).Needs(reject)

	_, err := sparkwing.RunWork(context.Background(), w)
	if err == nil {
		t.Fatal("ContinueOnError should preserve the failed rollup")
	}
	if !dependentRan.Load() {
		t.Fatal("ContinueOnError did not satisfy the dependent Needs edge")
	}
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

func TestRunWork_ParallelStepsRunConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseCtx, _ := newWorkCtx()
		var entered atomic.Int32
		release := make(chan struct{})
		var releaseOnce sync.Once
		enter := func(ctx context.Context) error {
			if entered.Add(1) == 2 {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		w := sparkwing.NewWork()
		sparkwing.Step(w, "a", enter)
		sparkwing.Step(w, "b", enter)
		sparkwing.Step(w, "c", enter)

		if _, err := sparkwing.RunWork(baseCtx, w); err != nil {
			t.Fatalf("RunWork: %v", err)
		}
		if got := entered.Load(); got < 2 {
			t.Fatalf("independent steps entered concurrently = %d, want at least 2", got)
		}
	})
}

func TestRunWork_FailFastCancelsSiblings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, _ := newWorkCtx()
		var siblingObserved atomic.Bool
		siblingStarted := make(chan struct{})

		w := sparkwing.NewWork()
		sparkwing.Step(w, "sibling", func(ctx context.Context) error {
			close(siblingStarted)
			<-ctx.Done()
			siblingObserved.Store(true)
			return ctx.Err()
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
	})
}

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

func TestRunWork_SpawnDispatchedThroughHandler(t *testing.T) {
	ctx, _ := newWorkCtx()
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	scan := sparkwing.JobSpawn(w, "scan", func(ctx context.Context) error { return nil }).Needs(a)
	var afterSawSpawn bool
	sparkwing.Step(w, "after", func(ctx context.Context) error {
		if scan.ResolvedID() == "test-node/scan" {
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
		sparkwing.RuntimePlumbing.Fns.SpawnSpecSetResolvedID(scan, parent+"/"+id)
		return nil, nil
	})

	hctx := sparkwingruntime.WithSpawnHandler(ctx, handler)
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

	hctx := sparkwingruntime.WithSpawnHandler(ctx, handler)
	if _, err := sparkwing.RunWork(hctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	for _, item := range items {
		if _, ok := seen.Load("shard-" + item); !ok {
			t.Errorf("handler missed item %q", item)
		}
	}
}
