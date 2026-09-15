package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: the three check classes and the wall time each promises. A tier that
// outgrows its class fails on the budget instead of growing quietly until
// somebody measures the hooks again.
const (
	preCommitBudget  = 3 * time.Second
	prePushBudget    = 10 * time.Second
	releaseCutBudget = 5 * time.Minute
)

const budgetStepID = "budget"

type stepTiming struct {
	id   string
	took time.Duration
}

type tierBudget struct {
	tier     string
	limit    time.Duration
	declared []sparkwing.WorkDep

	mu       sync.Mutex
	first    time.Time
	last     time.Time
	steps    []stepTiming
	stepFail bool
}

func newTierBudget(tier string, limit time.Duration) *tierBudget {
	return &tierBudget{tier: tier, limit: limit}
}

// safety: wrapping is the only way a step enters the budget, so a step
// declared with sparkwing.Step directly is one the verdict never sees. The
// tier tests fail a step the budget does not cover.
func (b *tierBudget) step(w *sparkwing.Work, id string, fn func(context.Context) error) *sparkwing.WorkStep {
	step := sparkwing.Step(w, id, func(ctx context.Context) error {
		started := time.Now()
		err := fn(ctx)
		b.record(id, started, time.Now(), err)
		return err
	})
	b.declared = append(b.declared, step)
	return step
}

// safety: Finally is what makes the verdict run after a fail-fast cancellation
// too, and the dependency on every budgeted step is what makes it run last.
func (b *tierBudget) verdict(w *sparkwing.Work) *sparkwing.WorkStep {
	return sparkwing.Step(w, budgetStepID, b.report).Needs(b.declared...).Finally()
}

func (b *tierBudget) record(id string, started, finished time.Time, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.first.IsZero() || started.Before(b.first) {
		b.first = started
	}
	if finished.After(b.last) {
		b.last = finished
	}
	b.steps = append(b.steps, stepTiming{id: id, took: finished.Sub(started)})
	if err != nil {
		b.stepFail = true
	}
}

func (b *tierBudget) report(ctx context.Context) error {
	b.mu.Lock()
	tier, limit, took := b.tier, b.limit, b.last.Sub(b.first)
	steps, stepFail := b.steps, b.stepFail
	b.mu.Unlock()

	line, err := budgetVerdict(tier, limit, took, steps, stepFail)
	if line != "" {
		sparkwing.Info(ctx, "%s", line)
	}
	return err
}

// safety: a red tier reports the failed check, not the budget. Its steps were
// cancelled part-way, so their durations measure the cancellation rather than
// the work, and a budget verdict on them would name the wrong cause.
func budgetVerdict(tier string, limit, took time.Duration, steps []stepTiming, stepFail bool) (string, error) {
	if len(steps) == 0 {
		return "", nil
	}
	slowest := steps[0]
	for _, s := range steps[1:] {
		if s.took > slowest.took {
			slowest = s
		}
	}
	line := fmt.Sprintf("%s ran %s of its %s budget; slowest step %q at %s",
		tier, took.Round(time.Millisecond), limit, slowest.id, slowest.took.Round(time.Millisecond))
	if stepFail || took <= limit {
		return line, nil
	}
	return line, fmt.Errorf("%s ran %s, over the %s this check class promises. The slowest step is %q at %s: "+
		"make it cheaper or move it to a slower class",
		tier, took.Round(time.Millisecond), limit, slowest.id, slowest.took.Round(time.Millisecond))
}
