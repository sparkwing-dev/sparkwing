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

// hack: a Go node's body is its Work, so run_node runs every step in this
// process and answers with the result step's output, as the in-process
// executor does.
func runNode(ctx context.Context, s *server, req nodeproto.Request) (any, error) {
	planned := s.plan()
	n := planned.Plan.Job(req.Node)
	if n == nil {
		return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownNode, Message: fmt.Sprintf("node %q is not in this process's plan", req.Node)}
	}
	if n.Work() == nil {
		return nil, &nodeproto.Error{Code: nodeproto.CodeBadRequest, Message: fmt.Sprintf("node %q has no body to run", req.Node)}
	}
	ctx = withFailure(s.nodeContext(ctx, planned, n, nil), req.Failure)
	return bodyResult(runBody(ctx, n)), nil
}

func withFailure(ctx context.Context, f *nodeproto.Failure) context.Context {
	if f == nil {
		return ctx
	}
	stage := sparkwing.StageAction
	if f.Stage == "verify" {
		stage = sparkwing.StageVerify
	}
	return sparkwing.WithFailure(ctx, sparkwing.Failure{Stage: stage, Err: errors.New(f.Message)})
}

// safety: a body's error is still a successful reply, since the body ran.
func bodyResult(out any, err error) nodeproto.RunResult {
	if err == nil && out != nil {
		var raw []byte
		if raw, err = json.Marshal(out); err == nil {
			return nodeproto.RunResult{Outcome: nodeproto.OutcomeSuccess, Output: raw}
		}
		err = fmt.Errorf("node output: %w", err)
	}
	if err != nil {
		return nodeproto.RunResult{Outcome: nodeproto.OutcomeFailed, Error: err.Error()}
	}
	return nodeproto.RunResult{Outcome: nodeproto.OutcomeSuccess}
}

// safety: a panic in a node body is the body failing, not the SDK, so it
// answers outcome failed instead of the failed reply serve's recover gives.
func runBody(ctx context.Context, n *sparkwing.JobNode) (out any, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("node %q panicked: %v", n.ID(), p)
		}
	}()
	if _, err := sparkwing.RunWork(ctx, n.Work()); err != nil {
		return nil, err
	}
	if rs := n.ResultStep(); rs != nil {
		return rs.Output(), nil
	}
	return nil, nil
}

// safety: the one context every node body, step body and closure runs under,
// so each sees the same logger, ids, inputs and dry-run as the old executor.
func (s *server) nodeContext(ctx context.Context, planned *Planned, n *sparkwing.JobNode, step *sparkwing.WorkStep) context.Context {
	ctx = sparkwingruntime.WithNode(sparkwingruntime.WithLogger(ctx, frameLogger{s}), n.ID())
	if step != nil {
		ctx = sparkwing.WithStep(ctx, step.ID())
		ctx = sparkwingruntime.WithLogger(ctx, stepLogger{frameLogger{s}, step.ID()})
	}
	ctx = sparkwingruntime.WithJSONResolver(ctx, func(id string) ([]byte, bool) { return localOutput(planned, id) })
	if in := planned.Plan.Inputs(); in != nil {
		ctx = sparkwingruntime.WithInputs(ctx, in)
	}
	if planned.Run.DryRun {
		ctx = sparkwingruntime.WithDryRun(ctx)
	}
	return ctx
}
