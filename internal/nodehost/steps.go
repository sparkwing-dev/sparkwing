package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func driveSteps(ctx context.Context, sess *ProtocolSession, sink *LogSink, n *NodeRun, attempt int) Result {
	children := map[string][]string{}
	for _, step := range n.Plan.Steps {
		for _, need := range step.Needs {
			children[need] = append(children[need], step.ID)
		}
	}
	skips := rangeSkips(n.Plan.Steps, children, n.Run.StartAt, n.Run.StopAt)
	graph := stepGraph{
		job: n.Plan,
		step: func(ctx context.Context, step nodeproto.StepSpec, settled []string) (json.RawMessage, string, string, error) {
			if reason := skips[step.ID]; reason != "" {
				return nil, "skipped", reason, nil
			}
			for _, closure := range step.Closures.SkipIf {
				var result nodeproto.EvalResult
				if err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: closure, Attempt: attempt, Settled: settled}, &result); err != nil {
					return nil, "", "", err
				}
				raw, err := evalValue(result, "skip_if")
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
			if n.Run.Context.DryRun && !step.HasDryRun && !step.SafeWithoutDryRun {
				return nil, "skipped", "no_dry_run_defined", nil
			}
			sink.host(sparkwing.LogRecord{Level: "info", Event: "step_start", Msg: step.ID, Attrs: map[string]any{"step": step.ID}})
			var result nodeproto.RunResult
			if err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpRunStep, Job: n.Plan.ID, Step: step.ID, Attempt: attempt, Settled: settled, Failure: n.Failure}, &result); err != nil {
				return nil, "", "", err
			}
			if err := executorResultFailure(result); err != nil {
				return nil, "", "", err
			}
			return result.Output, "passed", "", nil
		},
		record: func(ctx context.Context, id, state, reason string, started *time.Time, failure error) error {
			return recordHostedStep(ctx, sink, n, id, state, reason, started, failure)
		},
		cancel: func(ctx context.Context, id string) error {
			_, err := sess.Request(ctx, nodeproto.Request{Op: nodeproto.OpCancel, Job: n.Plan.ID, Step: id})
			return err
		},
		failFast: func(_ context.Context, trigger string, cancelled []string, latency time.Duration) error {
			sink.host(sparkwing.LogRecord{Level: "info", Event: "work_fail_fast", Msg: trigger, Attrs: map[string]any{
				"trigger_step": trigger, "cancelled_siblings": len(cancelled),
				"cancelled_steps": cancelled, "cancellation_latency_ms": latency.Milliseconds(),
			}})
			return nil
		},
	}
	output, err := graph.run(ctx, ctx)
	if err != nil {
		result := failed(err)
		var writeFailure *executorStepRecordError
		result.Final = errors.As(err, &writeFailure)
		return result
	}
	return Result{Outcome: sparkwing.Success, Output: output}
}

func recordHostedStep(ctx context.Context, sink *LogSink, n *NodeRun, id, state, reason string, started *time.Time, failure error) error {
	if state != "running" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
		defer cancel()
	}
	var err error
	switch state {
	case "running":
		err = n.Run.State.StartNodeStep(ctx, n.Run.ID, n.Plan.ID, id)
	case "skipped":
		err = n.Run.State.SkipNodeStep(ctx, n.Run.ID, n.Plan.ID, id)
	default:
		err = n.Run.State.FinishNodeStep(ctx, n.Run.ID, n.Plan.ID, id, state)
	}
	if err != nil {
		return &executorStepRecordError{err: err}
	}
	if state == "running" {
		return nil
	}
	event, level, outcome := "step_end", "info", state
	if state == store.StepPassed {
		outcome = "success"
	}
	attrs := map[string]any{"step": id, "outcome": outcome}
	if state == "skipped" {
		event = "step_skipped"
		if reason != "" {
			attrs["reason"] = reason
		}
	} else if started != nil {
		attrs["duration_ms"] = time.Since(*started).Milliseconds()
	}
	if failure != nil && state != store.StepCancelled {
		level, attrs["error"] = "error", failure.Error()
	}
	sink.host(sparkwing.LogRecord{Level: level, Event: event, Msg: id, Attrs: attrs})
	return nil
}
