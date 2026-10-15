package sparkwing

import (
	"context"
	"strings"
	"testing"
)

type producedJob struct {
	Base
	Produces[buildOut]
}

func (j *producedJob) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", j.run), nil
}

func (j *producedJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{Tag: "v9", Digest: "sha256:zzz"}, nil
}

type markerOnlyJob struct {
	Base
	Produces[buildOut]
}

func (j *markerOnlyJob) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", func(ctx context.Context) error { return nil })
	return nil, nil
}

type otherOut struct {
	Value string
}

type mismatchJob struct {
	Base
	Produces[otherOut]
}

func (j *mismatchJob) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", j.run), nil
}

func (j *mismatchJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{}, nil
}

type unmarkedTypedJob struct {
	Base
}

func (j *unmarkedTypedJob) Work(w *Work) (*WorkStep, error) {
	return Step(w, "run", j.run), nil
}

func (j *unmarkedTypedJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{}, nil
}

func TestProduces_AlignedJobRegistersOK(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "build", &producedJob{})
	if n.outType == nil {
		t.Fatal("aligned marker+SetResult job should have outType")
	}
	if got := n.outType.Name(); got != "buildOut" {
		t.Fatalf("outType = %q, want buildOut", got)
	}
}

func TestProduces_MarkerWithoutSetResultPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Produces[T] without typed return from Work should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") || !strings.Contains(msg, "Work") {
			t.Fatalf("panic should mention Produces and Work, got %q", msg)
		}
	}()
	Job(plan, "marker-only", &markerOnlyJob{})
}

func TestProduces_SetResultWithoutMarkerPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("typed Work return without Produces[T] should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") {
			t.Fatalf("panic should mention Produces, got %q", msg)
		}
	}()
	Job(plan, "unmarked", &unmarkedTypedJob{})
}

func TestProduces_MismatchPanics(t *testing.T) {
	plan := NewPlan()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("marker/Work mismatch should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") {
			t.Fatalf("panic message should mention Produces, got %q", msg)
		}
	}()
	Job(plan, "mismatch", &mismatchJob{})
}

func TestProduces_OnFailureRecoveryAppliesContract(t *testing.T) {
	plan := NewPlan()
	parent := Job(plan, "parent", jobFnNoop())
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("OnFailure recovery with marker-without-typed-return should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") || !strings.Contains(msg, "Work") {
			t.Fatalf("panic should mention Produces and Work, got %q", msg)
		}
	}()
	parent.OnFailure("recover", &markerOnlyJob{})
}

func TestOutput_WiresFromMarker(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "build", &producedJob{})
	ref := RefTo[buildOut](n)
	if ref.Job() != "build" {
		t.Fatalf("Ref.Job() = %q, want build", ref.Job())
	}
}

func TestOutput_PanicsOnUntypedNode(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "noop", jobFnNoop())
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("RefTo[T] on a node with no Produces[T] should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") {
			t.Fatalf("expected 'Produces[' in panic, got %q", msg)
		}
	}()
	_ = RefTo[buildOut](n)
}

func TestOutput_PanicsOnTypeMismatch(t *testing.T) {
	plan := NewPlan()
	n := Job(plan, "build", &producedJob{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("RefTo[T] with wrong T should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "produces") || !strings.Contains(msg, `"build"`) {
			t.Fatalf("panic should mention node id and produced type, got %q", msg)
		}
	}()
	_ = RefTo[otherOut](n)
}

type refConsumerJob struct {
	Base
	Build Ref[buildOut]
	Prev  Ref[buildOut]
}

func (j *refConsumerJob) Work(w *Work) (*WorkStep, error) {
	Step(w, "run", func(ctx context.Context) error { return nil })
	return nil, nil
}

func TestRefToField_AddsDependencyEdge(t *testing.T) {
	plan := NewPlan()
	produce := Job(plan, "produce", &producedJob{})
	consume := Job(plan, "consume", &refConsumerJob{
		Build: RefTo[buildOut](produce),
		Prev:  RefToLastRun[buildOut]("other-pipeline", "artifact"),
	})
	if got := consume.needs; len(got) != 1 || got[0] != "produce" {
		t.Fatalf("DepIDs = %v, want [produce]; a cross-pipeline ref is not an edge", got)
	}
}
