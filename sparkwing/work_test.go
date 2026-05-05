package sparkwing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestJobFn_SingleStepWork verifies that JobFn produces a Job whose
// Work has exactly one Step (id "run") and no typed result.
func TestJobFn_SingleStepWork(t *testing.T) {
	called := false
	job := sparkwing.JobFn(func(ctx context.Context) error {
		called = true
		return nil
	})
	w := job.Work()
	if w == nil {
		t.Fatal("JobFn.Work() returned nil")
	}
	steps := w.Steps()
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].ID() != "run" {
		t.Fatalf("step id = %q, want %q", steps[0].ID(), "run")
	}
	if w.ResultStep() != nil {
		t.Fatal("JobFn should not designate a result step")
	}
	// Smoke-execute the underlying fn.
	if _, err := steps[0].Fn()(context.Background()); err != nil {
		t.Fatalf("step fn returned %v", err)
	}
	if !called {
		t.Fatal("step fn did not invoke the wrapped closure")
	}
}

type fooOut struct {
	Tag string
}

// TestPlanJob_MaterializesWork verifies that sw.Job calls Work()
// at registration time so the inner DAG is reachable before dispatch.
type countingJob struct {
	calls int
}

func (j *countingJob) Work() *sparkwing.Work {
	j.calls++
	w := sparkwing.NewWork()
	w.Step("only", func(ctx context.Context) error { return nil })
	return w
}

func TestPlanJob_MaterializesWorkAtRegistration(t *testing.T) {
	plan := sparkwing.NewPlan()
	cj := &countingJob{}
	n := sparkwing.Job(plan, "compute", cj)
	if cj.calls != 1 {
		t.Fatalf("sw.Job should call Work() once at registration, got %d calls", cj.calls)
	}
	if n.ID() != "compute" {
		t.Fatalf("node id = %q, want compute", n.ID())
	}
}

// TestPlanJob_PanicsOnNilJobAndDuplicate locks down the input contract.
func TestPlanJob_PanicsOnNilJob(t *testing.T) {
	plan := sparkwing.NewPlan()
	defer func() {
		if recover() == nil {
			t.Fatal("sw.Job(nil) should panic")
		}
	}()
	sparkwing.Job(plan, "x", nil)
}

func TestPlanJob_PanicsOnDuplicateID(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	defer func() {
		if recover() == nil {
			t.Fatal("sw.Job duplicate id should panic")
		}
	}()
	sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
}

// TestWork_StepDuplicateIDPanics locks down the inner contract.
func TestWork_StepDuplicateIDPanics(t *testing.T) {
	w := sparkwing.NewWork()
	w.Step("only", func(ctx context.Context) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Work.Step id should panic")
		}
	}()
	w.Step("only", func(ctx context.Context) error { return nil })
}

// TestWork_StepNeeds wires step deps and reads them back.
func TestWork_StepNeeds(t *testing.T) {
	w := sparkwing.NewWork()
	a := w.Step("a", func(ctx context.Context) error { return nil })
	b := w.Step("b", func(ctx context.Context) error { return nil })
	c := w.Step("c", func(ctx context.Context) error { return nil }).Needs(a, b)
	deps := c.DepIDs()
	if len(deps) != 2 || deps[0] != "a" || deps[1] != "b" {
		t.Fatalf("c.DepIDs = %v, want [a b]", deps)
	}
	if len(b.DepIDs()) != 0 {
		t.Fatalf("b should have no deps, got %v", b.DepIDs())
	}
}

// TestWork_SequenceAndParallel mirrors the Plan-layer combinator
// behavior at the inner layer.
func TestWork_SequenceAndParallel(t *testing.T) {
	w := sparkwing.NewWork()
	a := w.Step("a", func(ctx context.Context) error { return nil })
	b := w.Step("b", func(ctx context.Context) error { return nil })
	c := w.Step("c", func(ctx context.Context) error { return nil })
	terminal := w.Sequence(a, b, c)
	if terminal != c {
		t.Fatal("Work.Sequence should return last step")
	}
	if got := b.DepIDs(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("b should depend on a, got %v", got)
	}
	if got := c.DepIDs(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("c should depend on b, got %v", got)
	}

	x := w.Step("x", func(ctx context.Context) error { return nil })
	y := w.Step("y", func(ctx context.Context) error { return nil })
	group := w.Parallel(x, y)
	join := w.Step("join", func(ctx context.Context) error { return nil }).Needs(group)
	if got := join.DepIDs(); len(got) != 2 {
		t.Fatalf("join should fan in 2 deps from a parallel group, got %v", got)
	}
}

// TestOut_TypedStep_ResolveAfterMarkDone verifies the typed Out / Get
// roundtrip blocks until the producing step posts its output.
func TestOut_TypedStep_ResolveAfterMarkDone(t *testing.T) {
	w := sparkwing.NewWork()
	want := fooOut{Tag: "shipped"}
	produced := sparkwing.Out(w, "produce", func(ctx context.Context) (fooOut, error) {
		return want, nil
	})
	// Drive the producing step's fn directly so we control timing.
	out, err := produced.WorkStep.Fn()(context.Background())
	if err != nil {
		t.Fatalf("produce fn returned %v", err)
	}
	produced.WorkStep.MarkDone(out)

	got := produced.Get(context.Background())
	if got != want {
		t.Fatalf("TypedStep.Get = %+v, want %+v", got, want)
	}
}

// TestSetResult_NonTypedStepPanics ensures that designating a
// non-typed step as the Work's result is caught at Plan-time.
func TestSetResult_NonTypedStepPanics(t *testing.T) {
	w := sparkwing.NewWork()
	s := w.Step("plain", func(ctx context.Context) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("SetResult on a non-typed step should panic")
		}
	}()
	w.SetResult(s)
}

// TestResult_RegistersTypedStepAndMarksResult verifies the Result[T]
// shortcut both creates the typed step and points the Work's result
// at it -- equivalent to Out + SetResult in one call.
func TestResult_RegistersTypedStepAndMarksResult(t *testing.T) {
	w := sparkwing.NewWork()
	want := fooOut{Tag: "ok"}
	step := sparkwing.Result(w, "run", func(ctx context.Context) (fooOut, error) {
		return want, nil
	})
	if step == nil || step.WorkStep == nil {
		t.Fatal("Result should return a non-nil TypedStep")
	}
	if w.ResultStep() != step.WorkStep {
		t.Fatalf("ResultStep = %v, want the step returned by Result", w.ResultStep())
	}
	if w.ResultType() == nil {
		t.Fatal("ResultType should be set to the typed step's output type")
	}
	if got := w.StepByID("run"); got != step.WorkStep {
		t.Fatalf("Result did not register step 'run' on the work")
	}
}

// TestSpawnNode_DeclaredAtPlanTime verifies that a SpawnNode shows up
// in the Work's Spawns() list so the Plan-time materializer has the
// full reachable graph before dispatch (orchestrator wiring lands in
// PR2).
func TestSpawnNode_DeclaredAtPlanTime(t *testing.T) {
	w := sparkwing.NewWork()
	a := w.Step("a", func(ctx context.Context) error { return nil })
	target := sparkwing.JobFn(func(ctx context.Context) error { return nil })
	h := w.SpawnNode("scan", target).Needs(a)
	if h == nil {
		t.Fatal("SpawnNode should return a handle")
	}
	spawns := w.Spawns()
	if len(spawns) != 1 || spawns[0].ID() != "scan" {
		t.Fatalf("expected one spawn 'scan', got %+v", spawns)
	}
	if got := spawns[0].DepIDs(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("spawn deps = %v, want [a]", got)
	}
}

// TestSpawnNode_PanicsOnEmptyOrNil is a contract guard.
func TestSpawnNode_PanicsOnEmptyID(t *testing.T) {
	w := sparkwing.NewWork()
	defer func() {
		if recover() == nil {
			t.Fatal("SpawnNode with empty id should panic")
		}
	}()
	w.SpawnNode("", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
}

// TestNodeForEach_RegistersExpansion verifies the new generator wires
// into the Plan's Expansion list and reads the source's typed output
// at dispatch time. This test does not run the orchestrator; it only
// exercises the Plan-time wiring.
type discoverJob struct {
	sparkwing.Produces[[]string]
	items []string
}

func (d *discoverJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	out := sparkwing.Out(w, "run", d.run)
	w.SetResult(out.WorkStep)
	return w
}

func (d *discoverJob) run(ctx context.Context) ([]string, error) { return d.items, nil }

func TestJobFanOutDynamic_RegistersExpansion(t *testing.T) {
	plan := sparkwing.NewPlan()
	src := sparkwing.Job(plan, "discover", &discoverJob{items: []string{"a", "b"}})
	calls := 0
	group := sparkwing.JobFanOutDynamic(plan, "builds", src, func(s string) (string, sparkwing.Workable) {
		calls++
		return "build-" + s, sparkwing.JobFn(func(ctx context.Context) error { return nil })
	})
	if group == nil {
		t.Fatal("JobFanOutDynamic should return a Group")
	}
	if !group.Dynamic() {
		t.Fatal("JobFanOutDynamic group should be dynamic")
	}
	if group.Name() != "builds" {
		t.Fatalf("group name = %q, want %q", group.Name(), "builds")
	}
	exps := plan.Expansions()
	if len(exps) != 1 {
		t.Fatalf("expected 1 expansion, got %d", len(exps))
	}
	if exps[0].Source != src {
		t.Fatal("expansion source should point at the discover node")
	}
	if calls != 0 {
		t.Fatalf("fn should not run at registration; ran %d times", calls)
	}
	// Resolve via an in-process Ref resolver to exercise the
	// reflection-driven slice traversal end-to-end.
	ctx := sparkwing.WithResolver(context.Background(), func(id string) (any, bool) {
		if id == "discover" {
			return []string{"a", "b", "c"}, true
		}
		return nil, false
	})
	children := exps[0].Gen(ctx)
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}
	wantIDs := []string{"build-a", "build-b", "build-c"}
	for i, c := range children {
		if c.ID() != wantIDs[i] {
			t.Fatalf("child %d id = %q, want %q", i, c.ID(), wantIDs[i])
		}
	}
	if calls != 3 {
		t.Fatalf("fn should run once per item; ran %d times", calls)
	}
}

// TestStepSkipIf_AccumulatesPredicates exercises the inner SkipIf
// surface and OR semantics expectation.
func TestStepSkipIf_AccumulatesPredicates(t *testing.T) {
	w := sparkwing.NewWork()
	s := w.Step("x", func(ctx context.Context) error { return nil }).
		SkipIf(func(ctx context.Context) bool { return false }).
		SkipIf(func(ctx context.Context) bool { return true })
	preds := s.SkipPredicates()
	if len(preds) != 2 {
		t.Fatalf("expected 2 predicates, got %d", len(preds))
	}
	if !preds[1](context.Background()) {
		t.Fatal("second predicate should report true")
	}
}

// Smoke: an error-returning step propagates the error through the
// step's underlying fn shape.
func TestWork_StepFnReturnsError(t *testing.T) {
	myErr := errors.New("boom")
	w := sparkwing.NewWork()
	s := w.Step("x", func(ctx context.Context) error { return myErr })
	if _, err := s.Fn()(context.Background()); !errors.Is(err, myErr) {
		t.Fatalf("step fn should propagate error, got %v", err)
	}
}
