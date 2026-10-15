package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type executorStepResult struct {
	id     string
	output json.RawMessage
	err    error
}

func (h *nodeExecutor) steps(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec) (json.RawMessage, error) {
	if len(job.Steps) == 0 {
		return nil, executorError(apierr.PlanInvalid, "executable job has no steps")
	}
	steps := make(map[string]nodeproto.StepSpec, len(job.Steps))
	pending := make(map[string]bool, len(job.Steps))
	finished := make(map[string]bool, len(job.Steps))
	failed := make(map[string]bool, len(job.Steps))
	running := map[string]bool{}
	cancelled := map[string]bool{}
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
	var output json.RawMessage
	fatal := false
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
			if (fatal || ctx.Err() != nil) && !step.Finally || ready && blocked && !step.Finally {
				delete(pending, step.ID)
				finished[step.ID] = true
				failed[step.ID] = true
				changed = true
				continue
			}
			if !ready {
				continue
			}
			delete(pending, step.ID)
			running[step.ID] = true
			changed = true
			go func() {
				raw, err := h.step(ctx, session, input, spec, step)
				done <- executorStepResult{id: step.ID, output: raw, err: err}
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
		if result.err == nil {
			if result.id == job.ResultStep {
				output = result.output
			}
			continue
		}
		failed[result.id] = true
		if !step.Optional && !cancelled[result.id] && firstErr == nil {
			firstErr = result.err
		}
		if job.FailurePolicy == "collect_all" || step.ContinueOnError || fatal {
			continue
		}
		fatal = true
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
	return output, firstErr
}

func (h *nodeExecutor) step(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec, step nodeproto.StepSpec) (json.RawMessage, error) {
	for _, closure := range step.Closures.SkipIf {
		raw, err := h.eval(ctx, session, input, closure, nil)
		if err != nil {
			return nil, err
		}
		var skip bool
		if string(raw) == "null" || json.Unmarshal(raw, &skip) != nil {
			return nil, executorError(apierr.ProtocolError, "step predicate did not return a boolean")
		}
		if skip {
			return nil, nil
		}
	}
	if spec.Resolved.Settings.Options.DryRun.Value && !step.HasDryRun && !step.SafeWithoutDryRun {
		return nil, nil
	}
	if !spec.Resolved.Settings.Options.DryRun.Value {
		for _, risk := range step.Risks {
			if !executorContains(spec.Resolved.Settings.Options.AllowRisk.Value, risk) {
				return nil, executorError(apierr.Forbidden, "step requires an unapproved risk")
			}
		}
	}
	var result nodeproto.RunResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: input.NodeID, Step: step.ID, Attempt: int(input.Attempt)}, &result); err != nil {
		return nil, err
	}
	if err := executorResultFailure(result); err != nil {
		return nil, err
	}
	if int64(len(result.Output)) > limits.NodeOutputBytes {
		return nil, executorError(apierr.PayloadTooLarge, "step output exceeds its limit")
	}
	if len(result.Output) > 0 && !json.Valid(result.Output) {
		return nil, errors.New("SDK step output is not JSON")
	}
	return result.Output, nil
}
