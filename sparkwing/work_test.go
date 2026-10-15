package sparkwing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestJobClosureBuildsOneStep(t *testing.T) {
	called := false
	plan := NewPlan()
	job := Job(plan, "single", func(context.Context) error { called = true; return nil })
	if job.resultStep != nil || len(job.work.steps) != 1 || job.work.steps[0].id != "run" {
		t.Fatalf("closure work = %+v", job.work)
	}
	if _, err := runStep(t.Context(), job.work, job.work.steps[0]); err != nil || !called {
		t.Fatalf("body called=%v, error=%v", called, err)
	}
}

type countingJob struct{ calls int }

func (job *countingJob) Work(work *Work) (*WorkStep, error) {
	job.calls++
	Step(work, "only", func(context.Context) error { return nil })
	return nil, nil
}

func TestJobMaterializesAtRegistration(t *testing.T) {
	plan := NewPlan()
	body := &countingJob{}
	Job(plan, "compute", body)
	if body.calls != 1 {
		t.Fatalf("materialization calls = %d", body.calls)
	}
}

func TestJobRejectsNilAndDuplicate(t *testing.T) {
	for name, makeJob := range map[string]func(){
		"nil": func() { Job(NewPlan(), "x", nil) },
		"duplicate": func() {
			plan := NewPlan()
			Job(plan, "x", jobFnNoop())
			Job(plan, "x", jobFnNoop())
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid job accepted")
				}
			}()
			makeJob()
		})
	}
}

func TestStepDependenciesAndGroups(t *testing.T) {
	work := NewWork()
	a := Step(work, "a", jobFnNoop())
	b := Step(work, "b", jobFnNoop())
	group := GroupSteps(work, "both", a, b)
	join := Step(work, "join", jobFnNoop()).Needs(group)
	if len(join.needs) != 2 || join.needs[0] != "a" || join.needs[1] != "b" {
		t.Fatalf("step dependencies = %v", join.needs)
	}
}

func TestTypedStepGetAfterRunStep(t *testing.T) {
	work := NewWork()
	step := Step(work, "produce", func(context.Context) (int, error) { return 42, nil })
	if _, err := runStep(t.Context(), work, step); err != nil {
		t.Fatal(err)
	}
	if got := StepGet[int](t.Context(), step); got != 42 {
		t.Fatalf("StepGet = %d", got)
	}
}

func TestStepGetRejectsUntypedStep(t *testing.T) {
	work := NewWork()
	step := Step(work, "plain", jobFnNoop())
	defer func() {
		if recover() == nil {
			t.Fatal("untyped StepGet accepted")
		}
	}()
	_ = StepGet[int](t.Context(), step)
}

func TestStepRejectsBadFunctionSignatures(t *testing.T) {
	cases := []struct {
		name string
		fn   any
		want string
	}{
		{"nil", nil, "non-nil"},
		{"non-function", "text", "must be a func"},
		{"variadic", func(context.Context, ...string) error { return nil }, "variadic"},
		{"no arguments", func() error { return nil }, "exactly 1 argument"},
		{"wrong argument", func(string) error { return nil }, "context.Context"},
		{"wrong result", func(context.Context) string { return "" }, "must return error"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				value := recover()
				if value == nil || !strings.Contains(value.(string), test.want) {
					t.Fatalf("panic = %v; want %s", value, test.want)
				}
			}()
			Step(NewWork(), "x", test.fn)
		})
	}
}

func TestStepReturnsBodyError(t *testing.T) {
	work := NewWork()
	want := errors.New("boom")
	step := Step(work, "x", func(context.Context) error { return want })
	if _, err := runStep(t.Context(), work, step); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}
