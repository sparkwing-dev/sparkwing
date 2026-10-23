package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type stepGraph struct {
	job      nodeproto.JobSpec
	skips    map[string]string
	step     func(context.Context, nodeproto.StepSpec, []string) (json.RawMessage, string, string, error)
	record   func(context.Context, string, string, string, *time.Time, error) error
	cancel   func(context.Context, string) error
	failFast func(context.Context, string, []string, time.Duration) error
}

type executorStepResult struct {
	id     string
	output json.RawMessage
	state  string
	reason string
	err    error
}

func (g stepGraph) run(ctx, parent context.Context) (json.RawMessage, error) {
	job := g.job
	ctx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	if len(job.Steps) == 0 {
		return nil, executorError(apierr.PlanInvalid, "executable job has no steps")
	}
	steps := make(map[string]nodeproto.StepSpec, len(job.Steps))
	pending := make(map[string]bool, len(job.Steps))
	finished := make(map[string]bool, len(job.Steps))
	failed := make(map[string]bool, len(job.Steps))
	running := map[string]bool{}
	cancelled := map[string]bool{}
	started := map[string]*time.Time{}
	for _, step := range job.Steps {
		if step.ID == "" || pending[step.ID] {
			return nil, executorError(apierr.PlanInvalid, "job has an absent or duplicate step ID")
		}
		steps[step.ID] = step
		pending[step.ID] = true
	}
	for _, step := range job.Steps {
		for _, need := range step.Needs {
			if _, ok := steps[need]; !ok {
				return nil, executorError(apierr.PlanInvalid, "step dependency is absent")
			}
		}
	}
	done := make(chan executorStepResult, len(job.Steps))
	var firstErr error
	var cleanupErr error
	var output json.RawMessage
	fatal := false
	writeFailed := false
	fatalStep := ""
	var failFastAt, lastCancelled time.Time
	var cancelledSteps []string
	var cancels sync.WaitGroup
	defer cancels.Wait()
	for len(pending) > 0 || len(running) > 0 {
		changed := false
		for _, step := range job.Steps {
			if !pending[step.ID] {
				continue
			}
			ready, blocked := true, false
			for _, need := range step.Needs {
				ready = ready && finished[need]
				blocked = blocked || failed[need] && !steps[need].ContinueOnError
			}
			if !ready {
				continue
			}
			if parent.Err() != nil || writeFailed || (fatal || ctx.Err() != nil) && !step.Finally || blocked && !step.Finally {
				if err := g.record(ctx, step.ID, "cancelled", "", nil, nil); err != nil {
					writeFailed = true
					firstErr = errors.Join(firstErr, err)
					abort(err)
				}
				delete(pending, step.ID)
				finished[step.ID] = true
				failed[step.ID] = true
				changed = true
				if fatalStep != "" && !step.Finally {
					cancelledSteps = append(cancelledSteps, step.ID)
					lastCancelled = time.Now()
				}
				continue
			}
			if reason, skip := g.skips[step.ID]; skip {
				if err := g.record(ctx, step.ID, "skipped", reason, nil, nil); err != nil {
					writeFailed = true
					failed[step.ID] = true
					firstErr = errors.Join(firstErr, err)
					abort(err)
				}
				delete(pending, step.ID)
				finished[step.ID], changed = true, true
				continue
			}
			delete(pending, step.ID)
			now := time.Now()
			started[step.ID] = &now
			writeCtx := ctx
			if step.Finally && ctx.Err() != nil {
				writeCtx = parent
			}
			if err := g.record(writeCtx, step.ID, "running", "", &now, nil); err != nil {
				writeFailed = true
				finished[step.ID], failed[step.ID], changed = true, true, true
				firstErr = errors.Join(firstErr, err)
				abort(err)
				continue
			}
			running[step.ID] = true
			changed = true
			settled := settledSteps(job.Steps, finished)
			go func() {
				stepCtx := ctx
				if step.Finally && ctx.Err() != nil {
					var cancel context.CancelFunc
					stepCtx, cancel = context.WithTimeout(parent, limits.CleanupTimeout)
					defer cancel()
				}
				raw, state, reason, err := g.step(stepCtx, step, settled)
				done <- executorStepResult{id: step.ID, output: raw, state: state, reason: reason, err: err}
			}()
		}
		if len(running) == 0 {
			if len(pending) > 0 && !changed {
				return nil, executorError(apierr.PlanInvalid, "step dependencies cannot make progress")
			}
			continue
		}
		result := <-done
		delete(running, result.id)
		finished[result.id] = true
		step := steps[result.id]
		state := result.state
		if result.err != nil {
			state = "failed"
			if cancelled[result.id] || errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
				state = "cancelled"
			}
		}
		if err := g.record(ctx, result.id, state, result.reason, started[result.id], result.err); err != nil {
			writeFailed = true
			result.err = errors.Join(result.err, err)
			abort(err)
		}
		if state == "cancelled" && cancelled[result.id] && fatalStep != "" {
			cancelledSteps = append(cancelledSteps, result.id)
			lastCancelled = time.Now()
		}
		if result.err == nil {
			if result.id == job.ResultStep {
				output = result.output
			}
			continue
		}
		failed[result.id] = true
		if step.Finally && ctx.Err() != nil && !step.Optional {
			cleanupErr = errors.Join(cleanupErr, result.err)
		}
		var recordFailure *executorStepRecordError
		if errors.As(result.err, &recordFailure) {
			firstErr = errors.Join(firstErr, result.err)
		} else if !step.Optional && !cancelled[result.id] && firstErr == nil {
			firstErr = result.err
		}
		if job.FailurePolicy == "collect_all" || step.ContinueOnError || fatal {
			continue
		}
		fatal = true
		if !errors.As(result.err, &recordFailure) {
			fatalStep, failFastAt = result.id, time.Now()
		}
		for id := range running {
			if steps[id].Finally {
				continue
			}
			cancelled[id] = true
			cancels.Add(1)
			go func() {
				defer cancels.Done()
				cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
				defer cancel()
				// safety: the original request remains joined, even if cooperative cancellation is refused.
				_ = g.cancel(cancelCtx, id)
			}()
		}
	}
	if fatalStep != "" {
		slices.Sort(cancelledSteps)
		var latency time.Duration
		if !lastCancelled.IsZero() {
			latency = max(0, lastCancelled.Sub(failFastAt))
		}
		if err := g.failFast(ctx, fatalStep, cancelledSteps, latency); err != nil {
			firstErr = errors.Join(firstErr, &executorStepRecordError{err: err})
		}
	}
	if cleanupErr != nil {
		cleanupErr = &executorCleanupError{err: cleanupErr}
	}
	return output, errors.Join(firstErr, cleanupErr, context.Cause(ctx))
}
