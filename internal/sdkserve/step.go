package sdkserve

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func runStep(ctx context.Context, s *server, req nodeproto.RequestOld) (any, error) {
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
	ctx, err := s.nodeContext(ctx, planned, node, step)
	if err != nil {
		return bodyResult(nil, err), nil
	}
	out, err := runStepBody(withFailure(ctx, req.Failure), work, step)
	if step != node.ResultStep() {
		out = nil
	}
	return bodyResult(out, err), nil
}

// safety: registered by the read loop before the run_step is dispatched, and
// a cancel that arrives first leaves a nil entry, so the step starts cancelled
// whichever request the SDK reads first.
func (s *server) track(ctx context.Context, req nodeproto.RequestOld) (context.Context, func()) {
	if req.Op != nodeproto.OpRunStepOld {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	key := [2]string{req.Node, req.Step}
	s.mu.Lock()
	if early, ok := s.steps[key]; ok && early == nil {
		cancel()
	}
	s.steps[key] = cancel
	s.mu.Unlock()
	return ctx, func() {
		s.mu.Lock()
		delete(s.steps, key)
		s.mu.Unlock()
		cancel()
	}
}

func cancelStep(_ context.Context, s *server, req nodeproto.RequestOld) (any, error) {
	key := [2]string{req.Node, req.Step}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.steps[key]; cancel != nil {
		cancel()
	} else {
		s.steps[key] = nil
	}
	return struct{}{}, nil
}

// safety: a body that panics ran and failed, which the host reads as outcome
// failed; the server's own recover would answer a failed reply instead.
func runStepBody(ctx context.Context, work *sparkwing.Work, step *sparkwing.WorkStep) (out any, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("step %q panicked: %v", step.ID(), p)
		}
	}()
	return sparkwing.RuntimePlumbing.Fns.WorkRunStep(ctx, work, step)
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
