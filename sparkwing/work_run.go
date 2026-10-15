package sparkwing

import (
	"context"
	"fmt"
)

func runStep(ctx context.Context, work *Work, step *WorkStep) (any, error) {
	if work == nil || step == nil || work.byID[step.id] != step {
		return nil, fmt.Errorf("sparkwing: run_step: step is not in the planned job")
	}
	for _, id := range step.needs {
		dependency := work.byID[id]
		if dependency == nil {
			return nil, fmt.Errorf("sparkwing: run_step: step dependency is absent")
		}
		// safety: a failed prerequisite can be allowed by ContinueOnError; nil
		// resolves StepGet without replacing a successful prior output.
		dependency.markDone(nil)
	}
	if IsDryRun(ctx) {
		if step.dryRunFn != nil {
			err := step.dryRunFn(ctx)
			step.markDone(nil)
			return nil, err
		}
		if !step.safeWithoutDryRun {
			return nil, fmt.Errorf("sparkwing: run_step: step has no dry-run body")
		}
	}
	out, err := step.fn(ctx)
	step.markDone(out)
	return out, err
}
