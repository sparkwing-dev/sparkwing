package sparkwing_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestJob_ClosureFormSingleStepWork verifies that sparkwing.Job
// accepts a func(ctx) error closure directly and wraps it as a Job
// whose Work has exactly one Step (id "run") and no typed result.
// This replaces the older sparkwing.JobFn explicit-wrapper path.
func TestJob_ClosureFormSingleStepWork(t *testing.T) {
	called := false
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "single", func(ctx context.Context) error {
		called = true
		return nil
	})
	if n.ResultStep() != nil {
		t.Fatal("closure-form Job should not have a typed result step")
	}
	steps := n.Work().Steps()
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].ID() != "run" {
		t.Fatalf("step id = %q, want %q", steps[0].ID(), "run")
	}
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

func (j *countingJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	j.calls++
	sparkwing.Step(w, "only", func(ctx context.Context) error { return nil })
	return nil, nil
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
	sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("sw.Job duplicate id should panic")
		}
	}()
	sparkwing.Job(plan, "a", func(ctx context.Context) error { return nil })
}

// TestWork_StepDuplicateIDPanics locks down the inner contract.
func TestWork_StepDuplicateIDPanics(t *testing.T) {
	w := sparkwing.NewWork()
	sparkwing.Step(w, "only", func(ctx context.Context) error { return nil })
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate sw.Step id should panic")
		}
	}()
	sparkwing.Step(w, "only", func(ctx context.Context) error { return nil })
}

// TestWork_StepNeeds wires step deps and reads them back.
func TestWork_StepNeeds(t *testing.T) {
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil })
	c := sparkwing.Step(w, "c", func(ctx context.Context) error { return nil }).Needs(a, b)
	deps := c.DepIDs()
	if len(deps) != 2 || deps[0] != "a" || deps[1] != "b" {
		t.Fatalf("c.DepIDs = %v, want [a b]", deps)
	}
	if len(b.DepIDs()) != 0 {
		t.Fatalf("b should have no deps, got %v", b.DepIDs())
	}
}

// TestWork_NeedsChainAndGroupSteps verifies the DAG shape:
// sequential deps via direct .Needs chains and fan-in via
// sparkwing.GroupSteps. Sequence/Parallel sugar verbs were dropped;
// this exercises the canonical primitive.
func TestWork_NeedsChainAndGroupSteps(t *testing.T) {
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil }).Needs(a)
	c := sparkwing.Step(w, "c", func(ctx context.Context) error { return nil }).Needs(b)
	if got := b.DepIDs(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("b should depend on a, got %v", got)
	}
	if got := c.DepIDs(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("c should depend on b, got %v", got)
	}

	x := sparkwing.Step(w, "x", func(ctx context.Context) error { return nil })
	y := sparkwing.Step(w, "y", func(ctx context.Context) error { return nil })
	group := sparkwing.GroupSteps(w, "fanout", x, y)
	if group.Name() != "fanout" {
		t.Fatalf("StepGroup.Name() = %q, want %q", group.Name(), "fanout")
	}
	join := sparkwing.Step(w, "join", func(ctx context.Context) error { return nil }).Needs(group)
	if got := join.DepIDs(); len(got) != 2 {
		t.Fatalf("join should fan in 2 deps from a step group, got %v", got)
	}
}

// TestGroupSteps_NeedsAndSkipIfDelegate verifies the *StepGroup
// modifier surface mirrors *WorkStep -- Needs and SkipIf delegate to
// every member.
func TestGroupSteps_NeedsAndSkipIfDelegate(t *testing.T) {
	w := sparkwing.NewWork()
	setup := sparkwing.Step(w, "setup", func(ctx context.Context) error { return nil })
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	b := sparkwing.Step(w, "b", func(ctx context.Context) error { return nil })
	g := sparkwing.GroupSteps(w, "members", a, b).
		Needs(setup).
		SkipIf(func(ctx context.Context) bool { return false })

	for _, m := range g.Members() {
		deps := m.DepIDs()
		if len(deps) != 1 || deps[0] != "setup" {
			t.Fatalf("group member %q should depend on setup, got %v", m.ID(), deps)
		}
		if len(m.SkipPredicates()) != 1 {
			t.Fatalf("group member %q should have one skip predicate, got %d", m.ID(), len(m.SkipPredicates()))
		}
	}
}

// TestStep_TypedStep_ResolveAfterMarkDone verifies the typed Step /
// StepGet roundtrip blocks until the producing step posts its output.
func TestStep_TypedStep_ResolveAfterMarkDone(t *testing.T) {
	w := sparkwing.NewWork()
	want := fooOut{Tag: "shipped"}
	produced := sparkwing.Step(w, "produce", func(ctx context.Context) (fooOut, error) {
		return want, nil
	})
	if produced.OutputType() == nil {
		t.Fatal("typed sw.Step should set OutputType from fn return")
	}
	// Drive the producing step's fn directly so we control timing.
	out, err := produced.Fn()(context.Background())
	if err != nil {
		t.Fatalf("produce fn returned %v", err)
	}
	produced.MarkDone(out)

	got := sparkwing.StepGet[fooOut](context.Background(), produced)
	if got != want {
		t.Fatalf("StepGet = %+v, want %+v", got, want)
	}
}

// TestStepGet_PanicsOnUntypedStep ensures StepGet enforces the
// step's typed-output contract at call time.
func TestStepGet_PanicsOnUntypedStep(t *testing.T) {
	w := sparkwing.NewWork()
	s := sparkwing.Step(w, "plain", func(ctx context.Context) error { return nil })
	s.MarkDone(nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("StepGet on an untyped step should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "no typed output") {
			t.Fatalf("panic should mention 'no typed output', got %q", msg)
		}
	}()
	_ = sparkwing.StepGet[fooOut](context.Background(), s)
}

// TestStepGet_PanicsOnTypeMismatch ensures StepGet rejects a T that
// doesn't match the producing step's outType.
func TestStepGet_PanicsOnTypeMismatch(t *testing.T) {
	w := sparkwing.NewWork()
	s := sparkwing.Step(w, "produce", func(ctx context.Context) (fooOut, error) {
		return fooOut{Tag: "ok"}, nil
	})
	out, _ := s.Fn()(context.Background())
	s.MarkDone(out)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("StepGet with mismatched T should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "produces") {
			t.Fatalf("panic should mention produced type, got %q", msg)
		}
	}()
	_ = sparkwing.StepGet[string](context.Background(), s)
}

// TestStep_RejectsBadFnSignatures locks down the reflection contract:
// fn must be func(ctx) error or func(ctx) (T, error).
func TestStep_RejectsBadFnSignatures(t *testing.T) {
	cases := []struct {
		name string
		fn   any
		want string
	}{
		{"nil", nil, "fn must be non-nil"},
		{"non-func", "not a func", "fn must be a func"},
		{"variadic", func(ctx context.Context, args ...string) error { return nil }, "must not be variadic"},
		{"zero args", func() error { return nil }, "exactly 1 argument"},
		{"two args", func(ctx context.Context, x int) error { return nil }, "exactly 1 argument"},
		{"wrong arg type", func(s string) error { return nil }, "argument must be context.Context"},
		{"one return non-error", func(ctx context.Context) string { return "" }, "one return value must return error"},
		{"three returns", func(ctx context.Context) (int, int, error) { return 0, 0, nil }, "must return error or (T, error)"},
		{"second return non-error", func(ctx context.Context) (int, string) { return 0, "" }, "second return value must be error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic containing %q", tc.want)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.want) {
					t.Fatalf("panic %q must contain %q", msg, tc.want)
				}
			}()
			w := sparkwing.NewWork()
			sparkwing.Step(w, "x", tc.fn)
		})
	}
}

// TestSpawnNode_DeclaredAtPlanTime verifies that a SpawnNode shows up
// in the Work's Spawns() list so the Plan-time materializer has the
// full reachable graph before dispatch (orchestrator wiring lands in
// PR2).
func TestSpawnNode_DeclaredAtPlanTime(t *testing.T) {
	w := sparkwing.NewWork()
	a := sparkwing.Step(w, "a", func(ctx context.Context) error { return nil })
	target := func(ctx context.Context) error { return nil }
	h := sparkwing.JobSpawn(w, "scan", target).Needs(a)
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
	sparkwing.JobSpawn(w, "", func(ctx context.Context) error { return nil })
}

// TestNodeForEach_RegistersExpansion verifies the new generator wires
// into the Plan's Expansion list and reads the source's typed output
// at dispatch time. This test does not run the orchestrator; it only
// exercises the Plan-time wiring.
type discoverJob struct {
	sparkwing.Produces[[]string]
	items []string
}

func (d *discoverJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", d.run), nil
}

func (d *discoverJob) run(ctx context.Context) ([]string, error) { return d.items, nil }

func TestJobFanOutDynamic_RegistersExpansion(t *testing.T) {
	plan := sparkwing.NewPlan()
	src := sparkwing.Job(plan, "discover", &discoverJob{items: []string{"a", "b"}})
	calls := 0
	group := sparkwing.JobFanOutDynamic(plan, "builds", src, func(s string) (string, any) {
		calls++
		return "build-" + s, func(ctx context.Context) error { return nil }
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
	s := sparkwing.Step(w, "x", func(ctx context.Context) error { return nil }).
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
	s := sparkwing.Step(w, "x", func(ctx context.Context) error { return myErr })
	if _, err := s.Fn()(context.Background()); !errors.Is(err, myErr) {
		t.Fatalf("step fn should propagate error, got %v", err)
	}
}

// SpawnNodeForEach validates fn signature at Plan time so
// a wrong-shaped fn panics during plan construction rather than
// during dispatch (which previously surfaced as a runtime spawn
// error after the parent's Needs cleared, much later than the
// structural mistake actually happened).

func TestSpawnNodeForEach_RejectsNonSliceItems(t *testing.T) {
	expectSpawnEachPanic(t, "items must be a slice", func() {
		w := sparkwing.NewWork()
		sparkwing.JobSpawnEach(w, "not-a-slice", func(s string) (string, any) {
			return "", nil
		})
	})
}

func TestSpawnNodeForEach_RejectsNonFuncFn(t *testing.T) {
	expectSpawnEachPanic(t, "fn must be a func", func() {
		w := sparkwing.NewWork()
		sparkwing.JobSpawnEach(w, []string{"a"}, "not-a-func")
	})
}

func TestSpawnNodeForEach_RejectsWrongArgCount(t *testing.T) {
	expectSpawnEachPanic(t, "fn must take exactly 1 argument", func() {
		w := sparkwing.NewWork()
		sparkwing.JobSpawnEach(w, []string{"a"}, func(a, b string) (string, sparkwing.Workable) {
			return "", nil
		})
	})
}

func TestSpawnNodeForEach_RejectsWrongReturnCount(t *testing.T) {
	expectSpawnEachPanic(t, "fn must return (string, sparkwing.Workable)", func() {
		w := sparkwing.NewWork()
		// The panic message lists both accepted shapes; we just need
		// "(string, sparkwing.Workable)" to appear somewhere in it.
		sparkwing.JobSpawnEach(w, []string{"a"}, func(s string) string { return "" })
	})
}

func TestSpawnNodeForEach_RejectsMismatchedItemType(t *testing.T) {
	expectSpawnEachPanic(t, "not assignable", func() {
		w := sparkwing.NewWork()
		sparkwing.JobSpawnEach(w, []int{1, 2, 3}, func(s string) (string, any) {
			return "", nil
		})
	})
}

func TestSpawnNodeForEach_RejectsNonStringFirstReturn(t *testing.T) {
	expectSpawnEachPanic(t, "first return value must be string", func() {
		w := sparkwing.NewWork()
		sparkwing.JobSpawnEach(w, []string{"a"}, func(s string) (int, sparkwing.Workable) {
			return 0, nil
		})
	})
}

func TestSpawnNodeForEach_RejectsNonWorkableSecondReturn(t *testing.T) {
	expectSpawnEachPanic(t, "must be sparkwing.Workable or func", func() {
		w := sparkwing.NewWork()
		// int is neither Workable nor func(ctx) error.
		sparkwing.JobSpawnEach(w, []string{"a"}, func(s string) (string, int) {
			return "", 0
		})
	})
}

func TestSpawnNodeForEach_AcceptsCorrectShape(t *testing.T) {
	w := sparkwing.NewWork()
	sparkwing.JobSpawnEach(w, []string{"a", "b"}, func(s string) (string, any) {
		return s, nil
	})
	if got := len(w.SpawnGens()); got != 1 {
		t.Fatalf("SpawnGens len = %d, want 1", got)
	}
}

// expectSpawnEachPanic runs body and asserts the panic value
// stringifies to something containing want.
func expectSpawnEachPanic(t *testing.T, want string, body func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q", want)
		}
		msg, _ := r.(string)
		if msg == "" {
			t.Fatalf("panic value not a string: %T %v", r, r)
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("panic %q must contain %q", msg, want)
		}
	}()
	body()
}
