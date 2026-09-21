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
	prePushBudget    = time.Minute
	releaseCutBudget = 5 * time.Minute
)

const budgetStepID = "budget"

// perf: what one core formats inside the commit tier's three seconds, at the
// 0.11 s a Go file costs in golangci-lint fmt. A wider change is accepted as
// long rather than failed: these steps cost per file, so a fileset several
// times a normal one costs several times its time with no tier having grown.
const budgetWaiverFiles = 25

type stepTiming struct {
	id   string
	took time.Duration
}

type tierBudget struct {
	tier     string
	limit    time.Duration
	scopeOf  scopeFunc
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

// safety: the scope is the tier's own, so the waiver counts the files the tier
// judged rather than a second reading of the change.
func (b *tierBudget) over(scopeOf scopeFunc) *tierBudget {
	b.scopeOf = scopeOf
	return b
}

// safety: a scope that cannot be read enforces the budget. A tier that stops
// knowing how wide its change is must not stop keeping its promise.
func (b *tierBudget) changedGoFiles(ctx context.Context) (int, string) {
	if b.scopeOf == nil {
		return -1, ""
	}
	files, scope, err := b.scopeOf(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return -1, ""
	}
	return len(files), scope
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

	changed, scope := b.changedGoFiles(ctx)
	line, err := budgetVerdict(tier, limit, took, steps, stepFail, changed)
	if line != "" {
		sparkwing.Info(ctx, "%s", line)
	}
	// safety: a tier whose scope is empty runs every step and passes every one
	// of them without judging a line of Go. That verdict reads exactly like a
	// full pass, so it says out loud what it covered.
	if changed == 0 {
		sparkwing.Warn(ctx, "%s", emptyScopeNotice(tier, scope))
	}
	return err
}

func emptyScopeNotice(tier, scope string) string {
	return fmt.Sprintf("%s judged no Go file: %s. This verdict covers nothing Go-side. "+
		"Work outside the scope named above is unjudged here; run `sparkwing run gate` to judge the whole tree",
		tier, scope)
}

// safety: a red tier reports the failed check, not the budget. Its steps were
// cancelled part-way, so their durations measure the cancellation rather than
// the work, and a budget verdict on them would name the wrong cause.
func budgetVerdict(tier string, limit, took time.Duration, steps []stepTiming, stepFail bool, changed int) (string, error) {
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
	if changed >= 0 {
		line += fmt.Sprintf(" over %d Go file(s)", changed)
	}
	if stepFail || took <= limit {
		return line, nil
	}
	if changed > budgetWaiverFiles {
		return line + fmt.Sprintf("; the budget is waived past %d Go files, because these steps cost per file",
			budgetWaiverFiles), nil
	}
	return line, fmt.Errorf("%s ran %s, over the %s this check class promises. The slowest step is %q at %s: "+
		"make it cheaper or move it to a slower class",
		tier, took.Round(time.Millisecond), limit, slowest.id, slowest.took.Round(time.Millisecond))
}
