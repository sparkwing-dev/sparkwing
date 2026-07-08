package sparkwing_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// --start-at on a linear DAG runs the named step + every downstream
// step; everything upstream is skipped.
func TestStepRange_LinearDAG_StartAt(t *testing.T) {
	var ranA, ranB, ranC atomic.Bool
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	sparkwing.Step(w, "c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwingruntime.WithStepRange(context.Background(), "b", "")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if ranA.Load() {
		t.Errorf("a should be skipped (upstream of --start-at=b)")
	}
	if !ranB.Load() {
		t.Errorf("b should run (--start-at=b)")
	}
	if !ranC.Load() {
		t.Errorf("c should run (downstream of b)")
	}
}

// --stop-at: the named step + every upstream step run; everything
// downstream is skipped.
func TestStepRange_LinearDAG_StopAt(t *testing.T) {
	var ranA, ranB, ranC atomic.Bool
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	sparkwing.Step(w, "c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwingruntime.WithStepRange(context.Background(), "", "b")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !ranA.Load() || !ranB.Load() {
		t.Errorf("a + b should run; got a=%v b=%v", ranA.Load(), ranB.Load())
	}
	if ranC.Load() {
		t.Errorf("c should be skipped (downstream of --stop-at=b)")
	}
}

// Pin the inclusive single-step window: --start-at X --stop-at X
// runs exactly X.
func TestStepRange_LinearDAG_StartEqualsStop(t *testing.T) {
	var ranA, ranB, ranC atomic.Bool
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	sparkwing.Step(w, "c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwingruntime.WithStepRange(context.Background(), "b", "b")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if ranA.Load() || ranC.Load() {
		t.Errorf("only b should run; got a=%v c=%v", ranA.Load(), ranC.Load())
	}
	if !ranB.Load() {
		t.Errorf("b should run")
	}
}

// Branching DAG: --start-at on a deep step skips all upstream
// including the parallel-branch sibling. Documents the
// reachability semantics from the ticket.
//
//	  root
//	 /    \
//	L      R
//	 \    /
//	 merge
//	   |
//	   end
//
// --start-at end skips root, L, R, merge -- only end runs.
func TestStepRange_BranchingDAG_StartAtSkipsAllUpstream(t *testing.T) {
	var ranRoot, ranL, ranR, ranMerge, ranEnd atomic.Bool
	w := sparkwing.NewWork()
	root := sparkwing.Step(w, "root", func(ctx context.Context) error { ranRoot.Store(true); return nil })
	left := sparkwing.Step(w, "L", func(ctx context.Context) error { ranL.Store(true); return nil }).Needs(root)
	right := sparkwing.Step(w, "R", func(ctx context.Context) error { ranR.Store(true); return nil }).Needs(root)
	merge := sparkwing.Step(w, "merge", func(ctx context.Context) error { ranMerge.Store(true); return nil }).Needs(left, right)
	sparkwing.Step(w, "end", func(ctx context.Context) error { ranEnd.Store(true); return nil }).Needs(merge)

	ctx := sparkwingruntime.WithStepRange(context.Background(), "end", "")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	for name, ran := range map[string]*atomic.Bool{"root": &ranRoot, "L": &ranL, "R": &ranR, "merge": &ranMerge} {
		if ran.Load() {
			t.Errorf("%s should be skipped (upstream of end)", name)
		}
	}
	if !ranEnd.Load() {
		t.Errorf("end should run")
	}
}

// User SkipIf still applies: range-skip OR'd with predicate. If the
// range says "run X" but the user's predicate returns true, X is
// still skipped (predicate wins for run vs skip; range-skip wins
// only over not-yet-evaluated predicates).
func TestStepRange_UserSkipIfStillApplies(t *testing.T) {
	var ranA, ranB atomic.Bool
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { ranA.Store(true); return nil })
	sparkwing.Step(w, "b", func(ctx context.Context) error { ranB.Store(true); return nil }).
		Needs(a).
		SkipIf(func(context.Context) bool { return true })

	ctx := sparkwingruntime.WithStepRange(context.Background(), "a", "b")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !ranA.Load() {
		t.Errorf("a should run (in range)")
	}
	if ranB.Load() {
		t.Errorf("b should be skipped (user SkipIf returned true)")
	}
}

// Empty range: every step runs. Pin the no-op contract.
func TestStepRange_NoBoundsRunsEverything(t *testing.T) {
	var ranA, ranB atomic.Bool
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { ranA.Store(true); return nil })
	sparkwing.Step(w, "b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)

	if _, err := sparkwing.RunWork(context.Background(), w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !ranA.Load() || !ranB.Load() {
		t.Errorf("both should run; got a=%v b=%v", ranA.Load(), ranB.Load())
	}
}

// Range bounds naming a step in another Work degrade gracefully:
// the local Work runs everything (no items match the bound, so the
// filter doesn't apply). Lets a multi-Job pipeline carry one global
// (start, stop) on ctx without forcing every Work to know about
// every other Work's step ids.
func TestStepRange_BoundInUnrelatedWorkIsNoOp(t *testing.T) {
	var ran atomic.Bool
	w := sparkwing.NewWork()
	sparkwing.Step(w, "local-step", func(ctx context.Context) error { ran.Store(true); return nil })

	ctx := sparkwingruntime.WithStepRange(context.Background(), "step-from-some-other-work", "")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !ran.Load() {
		t.Errorf("local-step should run; the bound names a step in a different Work")
	}
}

// TopologicalStepOrder returns a stable order consistent with Needs.
func TestTopologicalStepOrder_Stable(t *testing.T) {
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil }).Needs(a)
	sparkwing.Step(w, "c", func(ctx context.Context) error { return nil }).Needs(b)
	sparkwing.Step(w, "d", func(ctx context.Context) error { return nil }).Needs(a)

	got := w.TopologicalStepOrder()
	pos := func(id string) int {
		for i, x := range got {
			if x == id {
				return i
			}
		}
		return -1
	}
	if pos("a") < 0 || pos("b") < 0 || pos("c") < 0 || pos("d") < 0 {
		t.Fatalf("missing entries in topo order: %v", got)
	}
	if pos("a") >= pos("b") || pos("a") >= pos("d") {
		t.Errorf("a must precede its children: %v", got)
	}
	if pos("b") >= pos("c") {
		t.Errorf("b must precede c: %v", got)
	}
}
