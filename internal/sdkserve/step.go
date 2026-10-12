package sdkserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runStep(ctx context.Context, s *server, req nodeproto.Request) (any, error) {
	planned := s.plan()
	node := planned.Plan.Job(req.Node)
	if node == nil {
		return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownNode, Message: fmt.Sprintf("node %q is not in this process's plan", req.Node)}
	}
	work := node.Work()
	var step *sparkwing.WorkStep
	if work != nil {
		step = work.StepByID(req.Step)
	}
	if step == nil {
		return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownStep, Message: fmt.Sprintf("node %q has no step %q", req.Node, req.Step)}
	}
	ctx = nodeContext(ctx, s, planned, node.ID())
	ctx = sparkwingruntime.WithLogger(ctx, stepLogger{frameLogger{s}, step.ID()})
	if f := req.Failure; f != nil {
		stage := sparkwing.StageAction
		if f.Stage == "verify" {
			stage = sparkwing.StageVerify
		}
		ctx = sparkwing.WithFailure(ctx, sparkwing.Failure{Stage: stage, Err: errors.New(f.Message)})
	}
	out, err := runBody(ctx, work, step)
	if err == nil && step == node.ResultStep() {
		var output []byte
		if output, err = json.Marshal(out); err == nil {
			return nodeproto.RunResult{Outcome: nodeproto.OutcomeSuccess, Output: output}, nil
		}
		err = fmt.Errorf("encode output of step %q: %w", step.ID(), err)
	}
	return runResult(err), nil
}

func runResult(err error) nodeproto.RunResult {
	if err != nil {
		return nodeproto.RunResult{Outcome: nodeproto.OutcomeFailed, Error: err.Error()}
	}
	return nodeproto.RunResult{Outcome: nodeproto.OutcomeSuccess}
}

// safety: a body that panics ran and failed, which the host reads as outcome
// failed; the server's own recover would answer a failed reply instead.
func runBody(ctx context.Context, work *sparkwing.Work, step *sparkwing.WorkStep) (out any, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("step %q panicked: %v", step.ID(), p)
		}
	}()
	return sparkwing.RuntimePlumbing.Fns.WorkRunStep(ctx, work, step)
}

func nodeContext(ctx context.Context, s *server, planned *Planned, node string) context.Context {
	ctx = sparkwingruntime.WithLogger(ctx, frameLogger{s})
	ctx = sparkwingruntime.WithNode(ctx, node)
	if in := planned.Plan.Inputs(); in != nil {
		ctx = sparkwingruntime.WithInputs(ctx, in)
	}
	if planned.Run.DryRun {
		ctx = sparkwingruntime.WithDryRun(ctx)
	}
	return ctx
}

// safety: the host keeps a record's step only when the SDK sets it, and a
// command's output lines or a direct Log carry none of their own.
type stepLogger struct {
	frameLogger
	step string
}

func (l stepLogger) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l stepLogger) Emit(rec sparkwing.LogRecord) {
	if rec.Step == "" {
		rec.Step = l.step
	}
	l.frameLogger.Emit(rec)
}
