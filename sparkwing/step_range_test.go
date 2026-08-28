package sparkwing_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
