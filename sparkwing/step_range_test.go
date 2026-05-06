package sparkwing_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// IMP-007: --start-at on a linear DAG runs the named step + every
// downstream step; everything upstream is skipped.
func TestStepRange_LinearDAG_StartAt(t *testing.T) {
	var ranA, ranB, ranC atomic.Bool
	w := sparkwing.NewWork()
	a := w.Step("a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := w.Step("b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	w.Step("c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwing.WithStepRange(context.Background(), "b", "")
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
	a := w.Step("a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := w.Step("b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	w.Step("c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwing.WithStepRange(context.Background(), "", "b")
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
	a := w.Step("a", func(ctx context.Context) error { ranA.Store(true); return nil })
	b := w.Step("b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)
	w.Step("c", func(ctx context.Context) error { ranC.Store(true); return nil }).Needs(b)

	ctx := sparkwing.WithStepRange(context.Background(), "b", "b")
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
// --start-at end skips root, L, R, merge — only end runs.
func TestStepRange_BranchingDAG_StartAtSkipsAllUpstream(t *testing.T) {
	var ranRoot, ranL, ranR, ranMerge, ranEnd atomic.Bool
	w := sparkwing.NewWork()
	root := w.Step("root", func(ctx context.Context) error { ranRoot.Store(true); return nil })
	left := w.Step("L", func(ctx context.Context) error { ranL.Store(true); return nil }).Needs(root)
	right := w.Step("R", func(ctx context.Context) error { ranR.Store(true); return nil }).Needs(root)
	merge := w.Step("merge", func(ctx context.Context) error { ranMerge.Store(true); return nil }).Needs(left, right)
	w.Step("end", func(ctx context.Context) error { ranEnd.Store(true); return nil }).Needs(merge)

	ctx := sparkwing.WithStepRange(context.Background(), "end", "")
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
	a := w.Step("a", func(ctx context.Context) error { ranA.Store(true); return nil })
	w.Step("b", func(ctx context.Context) error { ranB.Store(true); return nil }).
		Needs(a).
		SkipIf(func(context.Context) bool { return true }) // user wants b skipped

	ctx := sparkwing.WithStepRange(context.Background(), "a", "b") // range includes b
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
	a := w.Step("a", func(ctx context.Context) error { ranA.Store(true); return nil })
	w.Step("b", func(ctx context.Context) error { ranB.Store(true); return nil }).Needs(a)

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
	w.Step("local-step", func(ctx context.Context) error { ran.Store(true); return nil })

	ctx := sparkwing.WithStepRange(context.Background(), "step-from-some-other-work", "")
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !ran.Load() {
		t.Errorf("local-step should run; the bound names a step in a different Work")
	}
}

// --- Plan-level validation surface ---

type stepRangePipe struct{ sparkwing.Base }

type stepRangeJob struct{ sparkwing.Base }

func (stepRangeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	a := w.Step("fetch", func(ctx context.Context) error { return nil })
	w.Step("compile", func(ctx context.Context) error { return nil }).Needs(a)
	return nil, nil
}

func (stepRangePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", stepRangeJob{})
	return nil
}

// Unknown --start-at is rejected with a Levenshtein suggestion,
// reusing the IMP-008 phrasing.
func TestValidateStepRange_UnknownIDSuggests(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp007-validate",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("imp007-validate")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp007-validate"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	got := sparkwing.ValidateStepRange(plan, "fetchh", "")
	if got == nil {
		t.Fatal("expected error for unknown step id")
	}
	for _, want := range []string{"--start-at", `"fetchh"`, `did you mean "fetch"`} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error missing %q\nfull: %s", want, got.Error())
		}
	}
}

// Known ids on both bounds → nil.
func TestValidateStepRange_KnownIDsOK(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp007-validate-ok",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("imp007-validate-ok")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp007-validate-ok"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := sparkwing.ValidateStepRange(plan, "fetch", "compile"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// Empty bounds = no-op. Pin the contract so we don't accidentally
// require both bounds in the future.
func TestValidateStepRange_EmptyBoundsNoOp(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp007-validate-empty",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("imp007-validate-empty")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp007-validate-empty"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := sparkwing.ValidateStepRange(plan, "", ""); err != nil {
		t.Errorf("empty bounds should be a no-op, got %v", err)
	}
}

// TopologicalStepOrder returns a stable order consistent with Needs.
func TestTopologicalStepOrder_Stable(t *testing.T) {
	w := sparkwing.NewWork()
	a := w.Step("a", func(ctx context.Context) error { return nil })
	b := w.Step("b", func(ctx context.Context) error { return nil }).Needs(a)
	w.Step("c", func(ctx context.Context) error { return nil }).Needs(b)
	w.Step("d", func(ctx context.Context) error { return nil }).Needs(a) // parallel branch

	got := w.TopologicalStepOrder()
	// 'a' must come before its descendants; relative order of b vs d
	// is stable on registration order (b before d).
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
