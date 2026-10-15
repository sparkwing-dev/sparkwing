package sparkwing

import (
	"context"
	"errors"
	"testing"
)

func TestRunStepPreservesDependencyOutput(t *testing.T) {
	work := NewWork()
	upstream := Step(work, "upstream", func(context.Context) (int, error) { return 42, nil })
	downstream := Step(work, "downstream", func(ctx context.Context) (int, error) {
		return StepGet[int](ctx, upstream) + 1, nil
	}).Needs(upstream)
	if output, err := runStep(t.Context(), work, upstream); err != nil || output != 42 {
		t.Fatalf("upstream = %v, %v", output, err)
	}
	if output, err := runStep(t.Context(), work, downstream); err != nil || output != 43 {
		t.Fatalf("downstream = %v, %v", output, err)
	}
}

func TestRunStepDryRun(t *testing.T) {
	ctx := context.WithValue(t.Context(), dryRunKey{}, true)
	work := NewWork()
	applied, previewed := false, false
	step := Step(work, "write", func(context.Context) error { applied = true; return nil }).
		DryRun(func(context.Context) error { previewed = true; return nil })
	if _, err := runStep(ctx, work, step); err != nil {
		t.Fatal(err)
	}
	if applied || !previewed {
		t.Fatalf("apply=%v preview=%v", applied, previewed)
	}
	unsafe := Step(work, "unsafe", func(context.Context) error { applied = true; return nil })
	if _, err := runStep(ctx, work, unsafe); err == nil || applied {
		t.Fatalf("unsafe step = %v, applied=%v", err, applied)
	}
	safe := Step(work, "safe", func(context.Context) error { applied = true; return nil }).SafeWithoutDryRun()
	if _, err := runStep(ctx, work, safe); err != nil || !applied {
		t.Fatalf("safe step = %v, applied=%v", err, applied)
	}
}

func TestRunStepReturnsBodyError(t *testing.T) {
	work := NewWork()
	want := errors.New("failed")
	step := Step(work, "fail", func(context.Context) error { return want })
	if _, err := runStep(t.Context(), work, step); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestWorkStepOptionalSetsContinueOnError(t *testing.T) {
	work := NewWork()
	step := Step(work, "optional", func(context.Context) error { return nil }).Optional()
	if !step.optional || !step.continueOnError {
		t.Fatalf("optional step flags: %+v", step)
	}
}
