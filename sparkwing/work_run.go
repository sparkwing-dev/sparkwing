package sparkwing

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (w *Work) beginStep(attempt int, current *WorkStep, settled []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if attempt < 1 || attempt < w.attempt || (attempt != w.attempt && len(w.active) != 0) || w.active[current.id] || settled == nil {
		return sdkError(apierr.ProtocolError, "step state is absent, stale or overlaps another attempt")
	}
	if attempt == w.attempt {
		current.mu.Lock()
		terminal := current.resolved
		current.mu.Unlock()
		if terminal {
			return sdkError(apierr.ProtocolError, "step is already terminal in this attempt")
		}
	}
	if err := w.validateSettlement(current, settled); err != nil {
		return err
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
	for _, id := range settled {
		w.byID[id].markDone(nil)
	}
	if w.active == nil {
		w.active = map[string]bool{}
	}
	w.active[current.id] = true
	return nil
}

func (w *Work) validateSettlement(current *WorkStep, settled []string) error {
	terminal := make(map[string]bool, len(settled))
	for _, id := range settled {
		if id == current.id || w.byID[id] == nil || terminal[id] || w.active[id] {
			return sdkError(apierr.ProtocolError, "settled steps contain an invalid or duplicate step")
		}
		terminal[id] = true
	}
	needs := append([]string(nil), current.needs...)
	checked := map[string]bool{current.id: true}
	for len(needs) > 0 {
		id := needs[len(needs)-1]
		needs = needs[:len(needs)-1]
		if !terminal[id] {
			return sdkError(apierr.ProtocolError, "step prerequisite is not settled")
		}
		if !checked[id] {
			checked[id] = true
			needs = append(needs, w.byID[id].needs...)
		}
	}
	return nil
}

func (w *Work) finishStep(id string) {
	w.mu.Lock()
	delete(w.active, id)
	w.mu.Unlock()
}

func runStep(ctx context.Context, work *Work, step *WorkStep) (any, error) {
	if work == nil || step == nil || work.byID[step.id] != step {
		return nil, fmt.Errorf("sparkwing: run_step: step is not in the planned job")
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
