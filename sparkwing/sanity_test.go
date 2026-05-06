package sparkwing_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// These tests exercise the SDK's public shape. They do not run a
// pipeline; the orchestrator that dispatches nodes lives in a separate
// package. The point here is to catch API regressions early.

type buildOut struct {
	Tag    string
	Digest string
}

type buildJob struct {
	sparkwing.Base
	sparkwing.Produces[buildOut]
}

func (b *buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", b.run), nil
}

func (b *buildJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{Tag: "v1", Digest: "sha256:abc"}, nil
}

type deployJob struct {
	sparkwing.Base
	Build sparkwing.Ref[buildOut]
}

func (d *deployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(ctx context.Context) error {
		_ = d.Build.Node()
		return nil
	})
	return nil, nil
}

func TestPlanJobAndNeeds(t *testing.T) {
	plan := sparkwing.NewPlan()
	build := sparkwing.Job(plan, "build", &buildJob{})
	deploy := sparkwing.Job(plan, "deploy", &deployJob{Build: sparkwing.RefTo[buildOut](build)}).Needs(build)
	_ = deploy

	if got := len(plan.Nodes()); got != 2 {
		t.Fatalf("expected 2 nodes, got %d", got)
	}
	d := plan.Node("deploy")
	if d == nil {
		t.Fatal("deploy node missing")
	}
	if deps := d.DepIDs(); len(deps) != 1 || deps[0] != "build" {
		t.Fatalf("deploy deps = %v, want [build]", deps)
	}
}

func TestTypedJobOutput(t *testing.T) {
	plan := sparkwing.NewPlan()
	build := sparkwing.Job(plan, "build", &buildJob{})
	if sparkwing.RefTo[buildOut](build).Node() != "build" {
		t.Fatalf("typed Ref not wired to node id")
	}
	if build.OutputType() == nil {
		t.Fatal("typed-output node should have an output type")
	}
}

func jobFnNoop() func(ctx context.Context) error {
	return func(ctx context.Context) error { return nil }
}

func TestParallel(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", jobFnNoop())
	b := sparkwing.Job(plan, "b", jobFnNoop())

	checks := sparkwing.GroupJobs(plan, "", a, b)
	d := sparkwing.Job(plan, "d", jobFnNoop()).Needs(checks)
	deps := d.DepIDs()
	if len(deps) != 2 {
		t.Fatalf("d should need both parallel members, got %v", deps)
	}
}

func TestPlanGroup_NamedMembership(t *testing.T) {
	plan := sparkwing.NewPlan()
	lint := sparkwing.Job(plan, "lint", jobFnNoop())
	test := sparkwing.Job(plan, "test", jobFnNoop())
	sparkwing.Job(plan, "other", jobFnNoop())

	checks := sparkwing.GroupJobs(plan, "safety", lint, test)
	if checks.Name() != "safety" {
		t.Fatalf("Group name: got %q, want safety", checks.Name())
	}
	publish := sparkwing.Job(plan, "publish", jobFnNoop()).Needs(checks)
	if got := len(publish.DepIDs()); got != 2 {
		t.Fatalf("publish should depend on both group members, got deps %v", publish.DepIDs())
	}
	if names := plan.NodeGroupNames("lint"); len(names) != 1 || names[0] != "safety" {
		t.Fatalf("lint group memberships: got %v, want [safety]", names)
	}
	if names := plan.NodeGroupNames("other"); len(names) != 0 {
		t.Fatalf("other should have no group memberships, got %v", names)
	}
}

func TestPlanGroup_UnnamedSkipped(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", jobFnNoop())
	b := sparkwing.Job(plan, "b", jobFnNoop())
	_ = sparkwing.GroupJobs(plan, "", a, b)
	_ = sparkwing.GroupJobs(plan, "", a, b)
	if names := plan.NodeGroupNames("a"); len(names) != 0 {
		t.Fatalf("unnamed groups should not contribute memberships, got %v", names)
	}
}

func TestPlanGroup_MultiMembership(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", jobFnNoop())
	b := sparkwing.Job(plan, "b", jobFnNoop())
	c := sparkwing.Job(plan, "c", jobFnNoop())
	_ = sparkwing.GroupJobs(plan, "g1", a, b)
	_ = sparkwing.GroupJobs(plan, "g2", a, c)
	names := plan.NodeGroupNames("a")
	if len(names) != 2 || names[0] != "g1" || names[1] != "g2" {
		t.Fatalf("a memberships: got %v, want [g1 g2]", names)
	}
}

type simplePipe struct{ sparkwing.Base }

func (simplePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, run sparkwing.RunContext) error {
	sparkwing.Job(plan, run.Pipeline, jobFnNoop())
	return nil
}

type plannerPipe struct{ sparkwing.Base }

func (plannerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, run sparkwing.RunContext) error {
	sparkwing.Job(plan, "one", jobFnNoop())
	sparkwing.Job(plan, "two", jobFnNoop()).Needs("one")
	return nil
}

func TestRegisterAndInvoke_OneNodePlan(t *testing.T) {
	plan := sparkwing.NewPlan()
	name := "sanity-simple"
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return simplePipe{} })

	reg, ok := sparkwing.Lookup(name)
	if !ok {
		t.Fatal("registered pipeline not found")
	}
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	nodes := plan.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("one-node Plan should produce 1 node, got %d", len(nodes))
	}
	if nodes[0].ID() != name {
		t.Fatalf("one-node Plan node id = %q, want %q", nodes[0].ID(), name)
	}
}

func TestRegisterAndInvoke_Planner(t *testing.T) {
	plan := sparkwing.NewPlan()
	name := "sanity-planner"
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return plannerPipe{} })
	reg, _ := sparkwing.Lookup(name)
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: name})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(plan.Nodes()) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(plan.Nodes()))
	}
	two := plan.Node("two")
	if two == nil || len(two.DepIDs()) != 1 || two.DepIDs()[0] != "one" {
		t.Fatalf("two should depend on one")
	}
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	name := "sanity-dup"
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return simplePipe{} })
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register should panic")
		}
	}()
	sparkwing.Register[sparkwing.NoInputs](name, func() sparkwing.Pipeline[sparkwing.NoInputs] { return simplePipe{} })
}

func TestLookup_Miss(t *testing.T) {
	if _, ok := sparkwing.Lookup("definitely-not-registered"); ok {
		t.Fatal("Lookup of missing pipeline should return false")
	}
}

func TestPlan_DuplicateIDPanics(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "one", jobFnNoop())
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate node id should panic")
		}
	}()
	sparkwing.Job(plan, "one", jobFnNoop())
}

func TestNeeds_AcceptsVariedForms(t *testing.T) {
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "a", jobFnNoop())
	b := sparkwing.Job(plan, "b", jobFnNoop())
	c := sparkwing.Job(plan, "c", jobFnNoop())
	group := sparkwing.GroupJobs(plan, "", a, b)
	d := sparkwing.Job(plan, "d", jobFnNoop()).Needs(c, group, "a")
	deps := d.DepIDs()
	// "a" is also in the group but should dedupe.
	if len(deps) != 3 {
		t.Fatalf("Needs dedup failed: got deps %v, want 3 entries (a,b,c in some order)", deps)
	}
}

func TestLogger_NopWhenUnset(t *testing.T) {
	ctx := context.Background()
	// Should not panic on a bare ctx (no logger installed).
	sparkwing.Info(ctx, "hello %s", "world")
	sparkwing.Error(ctx, "oh no")
}

type recordingLogger struct {
	lines []string
}

func (r *recordingLogger) Log(level, msg string) {
	r.lines = append(r.lines, level+": "+msg)
}

func (r *recordingLogger) Emit(rec sparkwing.LogRecord) {
	r.lines = append(r.lines, rec.Level+": "+rec.Msg)
}

func TestLogger_WritesThroughContext(t *testing.T) {
	l := &recordingLogger{}
	ctx := sparkwing.WithLogger(context.Background(), l)
	sparkwing.Info(ctx, "hi %s", "there")
	if len(l.lines) != 1 || l.lines[0] != "info: hi there" {
		t.Fatalf("unexpected logger output: %v", l.lines)
	}
}

// recordingEmitter captures full LogRecords so tests can assert on
// event/level/msg together. Used across the sparkwing_test package
// (sanity_test.go, fs_test.go).
type recordingEmitter struct {
	records []sparkwing.LogRecord
}

func (r *recordingEmitter) Log(level, msg string) {
	r.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}
func (r *recordingEmitter) Emit(rec sparkwing.LogRecord) {
	r.records = append(r.records, rec)
}

func TestRefPanicsWithoutResolver(t *testing.T) {
	r := sparkwing.Ref[buildOut]{NodeID: "build"}
	defer func() {
		if recover() == nil {
			t.Fatal("Ref.Get without resolver should panic")
		}
	}()
	r.Get(context.Background())
}

func TestRefResolves(t *testing.T) {
	expected := buildOut{Tag: "v2", Digest: "sha256:def"}
	ctx := sparkwing.WithResolver(context.Background(), func(id string) (any, bool) {
		if id == "build" {
			return expected, true
		}
		return nil, false
	})
	r := sparkwing.Ref[buildOut]{NodeID: "build"}
	got := r.Get(ctx)
	if got != expected {
		t.Fatalf("Ref.Get = %+v, want %+v", got, expected)
	}
}
