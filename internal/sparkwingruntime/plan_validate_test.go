package sparkwingruntime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepRangePipe struct{ sparkwing.Base }

type stepRangeJob struct{ sparkwing.Base }

func (stepRangeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	a := sparkwing.Step(w, "fetch", func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "compile", func(ctx context.Context) error { return nil }).Needs(a)
	return nil, nil
}

func (stepRangePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", stepRangeJob{})
	return nil
}

func TestValidateStepRange_UnknownIDSuggests(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("step-range-validate",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("step-range-validate")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "step-range-validate"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	got := sparkwingruntime.ValidateStepRange(plan, "fetchh", "")
	if got == nil {
		t.Fatal("expected error for unknown step id")
	}
	for _, want := range []string{"--sw-start-at", `"fetchh"`, `did you mean "fetch"`} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error missing %q\nfull: %s", want, got.Error())
		}
	}
}

func TestValidateStepRange_KnownIDsOK(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("step-range-validate-ok",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("step-range-validate-ok")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "step-range-validate-ok"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := sparkwingruntime.ValidateStepRange(plan, "fetch", "compile"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateStepRange_EmptyBoundsNoOp(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("step-range-validate-empty",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangePipe{} })
	reg, _ := sparkwing.Lookup("step-range-validate-empty")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "step-range-validate-empty"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := sparkwingruntime.ValidateStepRange(plan, "", ""); err != nil {
		t.Errorf("empty bounds should be a no-op, got %v", err)
	}
}
