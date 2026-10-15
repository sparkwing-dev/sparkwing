package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type executorStepResult struct {
	id     string
	output json.RawMessage
	state  string
	reason string
	err    error
}

func (h *nodeExecutor) steps(ctx, parent context.Context, session *executorSession, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec) (json.RawMessage, error) {
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
				if err := h.recordStep(ctx, input, spec, step.ID, "cancelled", "", nil, nil); err != nil {
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
			delete(pending, step.ID)
			now := time.Now()
			started[step.ID] = &now
			writeCtx := ctx
			if step.Finally && ctx.Err() != nil {
				writeCtx = parent
			}
			if err := h.recordStep(writeCtx, input, spec, step.ID, "running", "", &now, nil); err != nil {
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
				raw, state, reason, err := h.step(stepCtx, session, input, spec, step, settled)
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
		if err := h.recordStep(ctx, input, spec, result.id, state, result.reason, started[result.id], result.err); err != nil {
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
				var reply json.RawMessage
				// safety: the original request remains joined, even if cooperative cancellation is refused.
				_ = session.call(cancelCtx, nodeproto.Request{Op: nodeproto.OpCancel, Job: input.NodeID, Step: id}, &reply)
			}()
		}
	}
	if fatalStep != "" {
		slices.Sort(cancelledSteps)
		var latency time.Duration
		if !lastCancelled.IsZero() {
			latency = max(0, lastCancelled.Sub(failFastAt))
		}
		if err := h.diagnostic(ctx, input, "info", "work_fail_fast", "", fatalStep, map[string]any{
			"trigger_step": fatalStep, "cancelled_siblings": len(cancelledSteps),
			"cancelled_steps": cancelledSteps, "cancellation_latency_ms": latency.Milliseconds(),
		}); err != nil {
			firstErr = errors.Join(firstErr, &executorStepRecordError{err: err})
		}
	}
	if cleanupErr != nil {
		cleanupErr = &executorCleanupError{err: cleanupErr}
	}
	return output, errors.Join(firstErr, cleanupErr, context.Cause(ctx))
}

type executorCleanupError struct{ err error }

func (e *executorCleanupError) Error() string { return e.err.Error() }
func (e *executorCleanupError) Unwrap() error { return e.err }

func settledSteps(steps []nodeproto.StepSpec, finished map[string]bool) []string {
	settled := []string{}
	for _, step := range steps {
		if finished[step.ID] {
			settled = append(settled, step.ID)
		}
	}
	return settled
}

func (h *nodeExecutor) step(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec, step nodeproto.StepSpec, settled []string) (json.RawMessage, string, string, error) {
	for _, closure := range step.Closures.SkipIf {
		raw, err := h.eval(ctx, session, input, closure, nil, settled)
		if err != nil {
			return nil, "", "", err
		}
		var skip bool
		if string(raw) == "null" || json.Unmarshal(raw, &skip) != nil {
			return nil, "", "", executorError(apierr.ProtocolError, "step predicate did not return a boolean")
		}
		if skip {
			return nil, "skipped", "", nil
		}
	}
	if spec.Resolved.Settings.Options.DryRun.Value && !step.HasDryRun && !step.SafeWithoutDryRun {
		return nil, "skipped", "no_dry_run_defined", nil
	}
	if !spec.Resolved.Settings.Options.DryRun.Value {
		for _, risk := range step.Risks {
			if !executorContains(spec.Resolved.Settings.Options.AllowRisk.Value, risk) {
				return nil, "", "", executorError(apierr.Forbidden, "step requires an unapproved risk")
			}
		}
	}
	var result nodeproto.RunResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: input.NodeID, Step: step.ID, Attempt: int(input.Attempt), Settled: settled}, &result); err != nil {
		return nil, "", "", err
	}
	if err := executorResultFailure(result); err != nil {
		return nil, "", "", err
	}
	if len(result.Output) > 0 {
		if err := checkOutput(result.Output); err != nil {
			return nil, "", "", err
		}
	}
	return result.Output, "passed", "", nil
}

type executorStepRecordError struct{ err error }

func (e *executorStepRecordError) Error() string { return e.err.Error() }
func (e *executorStepRecordError) Unwrap() error { return e.err }

func (h *nodeExecutor) recordStep(ctx context.Context, input NodeInput, spec runspec.Spec, id, state, reason string, started *time.Time, failure error) error {
	if err := executorLostAuthority(ctx); err != nil {
		return err
	}
	report := store.StepReport{
		Attempt: runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: input.Generation, Attempt: input.Attempt},
		Actor:   executorActor(input), StepID: id, State: state, StartedAt: started,
	}
	if failure != nil {
		report.MaskedError = limits.Error(h.deps.Mask(failure.Error()))
	}
	if state != "running" {
		finished := time.Now()
		if report.StartedAt == nil {
			report.StartedAt = &finished
		}
		report.FinishedAt = &finished
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
		defer cancel()
	}
	if err := h.deps.Store.RecordStep(ctx, report); err != nil {
		return &executorStepRecordError{err: err}
	}
	if err := h.stepDiagnostic(ctx, input, report, reason); err != nil {
		return &executorStepRecordError{err: err}
	}
	return nil
}
