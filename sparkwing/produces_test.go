package sparkwing_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// SDK-013 introduced the Produces[T] marker; SDK-032 promoted it to a
// hard plan-time contract: a typed job MUST embed Produces[T] AND its
// Work() MUST call SetResult on a step of type T. Either piece alone
// is a Plan-time panic so the contract is visible at the type level
// AND honored at runtime. sw.RefTo[T] then validates against the
// marker and never falls back to inference.

type producedJob struct {
	sparkwing.Base
	sparkwing.Produces[buildOut]
}

func (j *producedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (j *producedJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{Tag: "v9", Digest: "sha256:zzz"}, nil
}

// markerOnlyJob declares Produces[T] but never returns a typed step
// from Work. The strict contract panics at Plan time when the marker
// is present without a matching Work return value.
type markerOnlyJob struct {
	sparkwing.Base
	sparkwing.Produces[buildOut]
}

func (j *markerOnlyJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", func(ctx context.Context) error { return nil })
	return nil, nil
}

// otherOut is a distinct type used to provoke marker/Work mismatches.
type otherOut struct {
	Value string
}

type mismatchJob struct {
	sparkwing.Base
	sparkwing.Produces[otherOut]
}

func (j *mismatchJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (j *mismatchJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{}, nil
}

// unmarkedTypedJob returns a typed *WorkStep from Work but
// deliberately omits the Produces[T] marker. This is a Plan-time
// panic.
type unmarkedTypedJob struct {
	sparkwing.Base
}

func (j *unmarkedTypedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (j *unmarkedTypedJob) run(ctx context.Context) (buildOut, error) {
	return buildOut{}, nil
}

func TestProduces_AlignedJobRegistersOK(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &producedJob{})
	if n.OutputType() == nil {
		t.Fatal("aligned marker+SetResult job should have outType")
	}
	if got := n.OutputType().Name(); got != "buildOut" {
		t.Fatalf("outType = %q, want buildOut", got)
	}
}

func TestProduces_MarkerWithoutSetResultPanics(t *testing.T) {
	plan := sparkwing.NewPlan()
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
	sparkwing.Job(plan, "marker-only", &markerOnlyJob{})
}

func TestProduces_SetResultWithoutMarkerPanics(t *testing.T) {
	plan := sparkwing.NewPlan()
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
	sparkwing.Job(plan, "unmarked", &unmarkedTypedJob{})
}

func TestProduces_MismatchPanics(t *testing.T) {
	plan := sparkwing.NewPlan()
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
	sparkwing.Job(plan, "mismatch", &mismatchJob{})
}

// SDK-035: the same Produces/SetResult contract that Job enforces
// must also fire on the detached-node paths -- OnFailure recovery
// nodes, JobFanOutDynamic children, orchestrator SpawnNode dispatch.
// Before SDK-035 these silently skipped the check.
func TestProduces_OnFailureRecoveryAppliesContract(t *testing.T) {
	plan := sparkwing.NewPlan()
	parent := sparkwing.Job(plan, "parent", jobFnNoop())
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

func TestProduces_NewDetachedNodeAppliesContract(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewDetachedNode with marker-without-typed-return should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Produces[") || !strings.Contains(msg, "Work") {
			t.Fatalf("panic should mention Produces and Work, got %q", msg)
		}
	}()
	sparkwing.NewDetachedNode("spawn-child", &markerOnlyJob{})
}

func TestOutput_WiresFromMarker(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &producedJob{})
	ref := sparkwing.RefTo[buildOut](n)
	if ref.Node() != "build" {
		t.Fatalf("Ref.Node() = %q, want build", ref.Node())
	}
}

func TestOutput_PanicsOnUntypedNode(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "noop", jobFnNoop())
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
	_ = sparkwing.RefTo[buildOut](n)
}

func TestOutput_PanicsOnTypeMismatch(t *testing.T) {
	plan := sparkwing.NewPlan()
	n := sparkwing.Job(plan, "build", &producedJob{})
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
	_ = sparkwing.RefTo[otherOut](n)
}

// LintWarnings used to flag missing Produces / missing SetResult; under
// SDK-032 those are hard panics, so an aligned plan should produce no
// warnings.
func TestLintWarning_NoneWhenAligned(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "build", &producedJob{})
	sparkwing.Job(plan, "noop", jobFnNoop())

	if warns := plan.LintWarnings(); len(warns) != 0 {
		t.Fatalf("expected 0 lint warnings, got %+v", warns)
	}
}
