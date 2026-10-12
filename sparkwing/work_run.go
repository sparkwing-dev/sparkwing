package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// RunWork executes w's step DAG. Items run in dependency
// order (declared via Needs); independent items run in parallel.
//
// For each successful WorkStep the typed output is recorded via
// MarkDone so downstream sparkwing.StepGet[T](ctx, step) calls resolve.
// RunWork itself returns (nil, err); the Job's typed output is
// recorded on the *WorkStep the Job's Work returned and read back by
// the orchestrator via Job.ResultStep().Output().
//
// Emits step_start / step_end / step_skipped LogRecord events for
// each executed step (Attrs: id, kind, duration_ms,
// outcome, optional error).
//
// FailFast cancels ordinary siblings after the first decisive error;
// CollectAll lets ready work finish. Finally steps survive sibling
// cancellation but still honor cancellation of the parent context.
// Skipped steps (any SkipIf returns true) propagate to downstream as
// if they succeeded with no output.
func RunWork(ctx context.Context, w *Work) (any, error) {
	if w == nil {
		return nil, nil
	}
	steps := w.Steps()
	if len(steps) == 0 {
		return nil, nil
	}

	items := make(map[string]*workItem, len(steps))
	for _, s := range steps {
		if _, exists := items[s.ID()]; exists {
			panic(fmt.Sprintf("sparkwing: RunWork: duplicate step id %q", s.ID()))
		}
		items[s.ID()] = &workItem{
			id:     s.ID(),
			deps:   s.DepIDs(),
			step:   s,
			skipIf: s.SkipPredicates(),
		}
	}

	indeg := make(map[string]int, len(items))
	children := make(map[string][]string, len(items))
	for id, it := range items {
		if _, ok := indeg[id]; !ok {
			indeg[id] = 0
		}
		for _, d := range it.deps {
			if _, ok := items[d]; !ok {
				return nil, fmt.Errorf("sparkwing: RunWork: item %q depends on unknown item %q", id, d)
			}
			indeg[id]++
			children[d] = append(children[d], id)
		}
	}

	if r, ok := stepRangeFromContext(ctx); ok && (r.start != "" || r.stop != "") {
		_, hasStart := items[r.start]
		_, hasStop := items[r.stop]
		if r.start == "" || hasStart || r.stop == "" || hasStop {
			if (r.start != "" && hasStart) || (r.stop != "" && hasStop) {
				skips := computeStepRangeSkips(items, children, r)
				for id, reason := range skips {
					if it, ok := items[id]; ok {
						it.rangeSkipReason = reason
					}
				}
			}
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	policy := w.ParallelFailurePolicy()

	done := make(chan stepResult, len(items))
	pending := make(map[string]bool, len(items))
	for id := range items {
		pending[id] = true
	}

	var (
		mu                 sync.Mutex
		firstErr           error
		fatalErr           error
		fatalStep          string
		failFastAt         time.Time
		cancelledSiblings  int
		cancelledSteps     []string
		cancellationFinish time.Time
		running            int
	)
	setErr := func(it *workItem, err error) {
		mu.Lock()
		defer mu.Unlock()
		step := it.step
		if !step.IsOptional() {
			if firstErr == nil {
				firstErr = err
			}
		}
		collect := policy == CollectAll
		if !collect && !step.IsContinueOnError() {
			if fatalErr == nil {
				fatalErr = err
				fatalStep = it.id
				failFastAt = time.Now()
			}
			cancel()
		}
	}
	setRunCancellation := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	getErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
	}
	getFatalErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return fatalErr
	}
	cancelPending := func() {
		for id := range pending {
			it := items[id]
			if it.isFinally() {
				continue
			}
			delete(pending, id)
			cancelledSiblings++
			cancelledSteps = append(cancelledSteps, id)
			cancellationFinish = time.Now()
			emitStepTerminal(ctx, id, 0, context.Canceled, true)
			for _, childID := range children[id] {
				if items[childID].isFinally() {
					indeg[childID]--
				}
			}
		}
	}

	schedule := func() {
		ready := make([]*workItem, 0, len(pending))
		for id := range pending {
			if indeg[id] == 0 && (getFatalErr() == nil || items[id].isFinally()) {
				ready = append(ready, items[id])
			}
		}
		for _, it := range ready {
			delete(pending, it.id)
			running++
			itemCtx := runCtx
			if it.isFinally() {
				itemCtx = ctx
			}
			go runOneItem(itemCtx, it, done)
		}
	}

	schedule()
	if running == 0 {
		return nil, fmt.Errorf("sparkwing: RunWork: cycle detected -- no item has zero in-degree (items=%d)", len(items))
	}

	for running > 0 {
		res := <-done
		running--

		if res.err != nil {
			it := items[res.id]
			if res.cancelled && getFatalErr() != nil {
				cancelledSiblings++
				cancelledSteps = append(cancelledSteps, res.id)
				cancellationFinish = time.Now()
			} else if res.cancelled && ctx.Err() != nil {
				setRunCancellation(ctx.Err())
				cancelPending()
			} else {
				setErr(it, res.err)
				if getFatalErr() != nil {
					cancelPending()
				}
			}
			dependencySatisfied := it.step.IsContinueOnError()
			for _, c := range children[res.id] {
				if dependencySatisfied || items[c].isFinally() {
					indeg[c]--
				}
			}
			schedule()
			continue
		}
		for _, c := range children[res.id] {
			indeg[c]--
		}
		schedule()
	}

	if fatalStep != "" {
		sort.Strings(cancelledSteps)
		latency := time.Duration(0)
		if !cancellationFinish.IsZero() {
			latency = cancellationFinish.Sub(failFastAt)
		}
		LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
			TS:    time.Now(),
			Level: "info",
			Event: EventWorkFailFast,
			Msg:   fatalStep,
			Attrs: map[string]any{
				"trigger_step":            fatalStep,
				"cancelled_siblings":      cancelledSiblings,
				"cancelled_steps":         cancelledSteps,
				"cancellation_latency_ms": latency.Milliseconds(),
			},
		}))
	}

	if err := getErr(); err != nil {
		return nil, err
	}
	return nil, nil
}

type workItem struct {
	id     string
	deps   []string
	skipIf []SkipPredicate
	step   *WorkStep

	rangeSkipReason string
}

func (it *workItem) isFinally() bool {
	return it != nil && it.step != nil && it.step.IsFinally()
}

func runOneItem(ctx context.Context, it *workItem, done chan<- stepResult) {
	defer func() {
		if r := recover(); r != nil {
			done <- stepResult{id: it.id, err: fmt.Errorf("item %q panicked: %v", it.id, r)}
		}
	}()

	if it.rangeSkipReason != "" {
		emitStepSkippedWithReason(ctx, it.id, it.rangeSkipReason)
		it.markDone(nil)
		done <- stepResult{id: it.id}
		return
	}

	for _, p := range it.skipIf {
		if p(ctx) {
			emitStepEvent(ctx, it.id, "step_skipped", 0, nil)
			it.markDone(nil)
			done <- stepResult{id: it.id}
			return
		}
	}

	if IsDryRun(ctx) && it.step.dryRunFn == nil && !it.step.safeWithoutDryRun {
		emitStepSkippedWithReason(ctx, it.id, "no_dry_run_defined")
		it.markDone(nil)
		done <- stepResult{id: it.id}
		return
	}

	start := time.Now()
	emitStepEvent(ctx, it.id, "step_start", 0, nil)

	stepCtx := WithStep(ctx, it.id)
	out, err := dispatchItem(stepCtx, it)
	elapsed := time.Since(start)

	if err != nil {
		cancelled := ctx.Err() != nil && errors.Is(err, context.Canceled)
		emitStepTerminal(ctx, it.id, elapsed, err, cancelled)
		done <- stepResult{id: it.id, err: &StepError{StepID: it.id, Cause: err}, cancelled: cancelled}
		return
	}
	it.markDone(out)
	emitStepEvent(ctx, it.id, "step_end", elapsed, nil)
	done <- stepResult{id: it.id, out: out}
}

func dispatchItem(ctx context.Context, it *workItem) (any, error) {
	if IsDryRun(ctx) && it.step.dryRunFn != nil {
		return nil, it.step.dryRunFn(ctx)
	}
	return it.step.fn(ctx)
}

func (it *workItem) markDone(out any) { it.step.markDone(out) }

type stepResult struct {
	id        string
	out       any
	err       error
	cancelled bool
}

// StepError wraps a step body's error with the originating step ID.
// Loggers can use errors.As to pull the StepID off and tag the log
// record with the step in its breadcrumb -- otherwise the runner-side
// inline error log would render at the bare node level even though
// the surrounding step output is in the same step's context.
type StepError struct {
	StepID string
	Cause  error
}

func (e *StepError) Error() string {
	if e == nil || e.Cause == nil {
		return ""
	}
	return fmt.Sprintf("step %q: %s", e.StepID, e.Cause.Error())
}

func (e *StepError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func emitStepEvent(ctx context.Context, stepID, event string, elapsed time.Duration, err error) {
	attrs := map[string]any{"step": stepID}
	if elapsed > 0 || event == "step_end" {
		attrs["duration_ms"] = elapsed.Milliseconds()
	}
	level := "info"
	if err != nil {
		attrs["error"] = err.Error()
		attrs["outcome"] = "failed"
		level = "error"
	} else if event == "step_end" {
		attrs["outcome"] = "success"
	} else if event == "step_skipped" {
		attrs["outcome"] = "skipped"
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: level,
		JobID: NodeFromContext(ctx),
		Event: event,
		Msg:   stepID,
		Attrs: attrs,
	}))
}

func emitStepTerminal(ctx context.Context, stepID string, elapsed time.Duration, err error, cancelled bool) {
	if !cancelled {
		emitStepEvent(ctx, stepID, "step_end", elapsed, err)
		return
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: NodeFromContext(ctx),
		Event: "step_end",
		Msg:   stepID,
		Attrs: map[string]any{
			"step":        stepID,
			"duration_ms": elapsed.Milliseconds(),
			"outcome":     "cancelled",
		},
	}))
}
