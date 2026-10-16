package nodehost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (h *nodeExecutor) steps(ctx, parent context.Context, session *executorSession, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec) (json.RawMessage, error) {
	children := map[string][]string{}
	for _, step := range job.Steps {
		for _, need := range step.Needs {
			children[need] = append(children[need], step.ID)
		}
	}
	options := spec.Resolved.Settings.Options
	graph := stepGraph{
		skips: rangeSkips(job.Steps, children, options.StartAt.Value, options.StopAt.Value),
		job:   job,
		step: func(ctx context.Context, step nodeproto.StepSpec, settled []string) (json.RawMessage, string, string, error) {
			return h.step(ctx, session, input, spec, step, settled)
		},
		record: func(ctx context.Context, id, state, reason string, started *time.Time, failure error) error {
			return h.recordStep(ctx, input, spec, id, state, reason, started, failure)
		},
		cancel: func(ctx context.Context, id string) error {
			var reply json.RawMessage
			return session.call(ctx, nodeproto.Request{Op: nodeproto.OpCancel, Job: input.NodeID, Step: id}, &reply, "")
		},
		failFast: func(ctx context.Context, trigger string, cancelled []string, latency time.Duration) error {
			return h.diagnostic(ctx, input, "info", "work_fail_fast", "", trigger, map[string]any{
				"trigger_step": trigger, "cancelled_siblings": len(cancelled),
				"cancelled_steps": cancelled, "cancellation_latency_ms": latency.Milliseconds(),
			})
		},
	}
	return graph.run(ctx, parent)
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
		raw, err := h.eval(ctx, session, input, closure, "skip_if", nil, settled)
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
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: input.NodeID, Step: step.ID, Attempt: int(input.Attempt), Settled: settled}, &result, ""); err != nil {
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
