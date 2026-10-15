package sparkwing

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (w *Work) beginAttempt(attempt int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if attempt < 1 || attempt < w.attempt || (attempt != w.attempt && w.active != 0) {
		return sdkError(apierr.ProtocolError, "run_step attempt is stale or overlaps another attempt")
	}
	if attempt != w.attempt {
		// safety: StepGet must never return output left by an earlier attempt.
		for _, step := range w.steps {
			step.mu.Lock()
			step.resolved, step.out, step.done = false, nil, nil
			step.mu.Unlock()
		}
		w.attempt = attempt
	}
	w.active++
	return nil
}

func (w *Work) finishAttempt() {
	w.mu.Lock()
	w.active--
	w.mu.Unlock()
}

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
