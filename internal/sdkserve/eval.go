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

type afterRunInput struct {
	Outcome nodeproto.Outcome `json:"outcome"`
	Error   string            `json:"error,omitempty"`
}

func eval(ctx context.Context, s *server, req nodeproto.Request) (any, error) {
	planned := s.plan()
	c, ok := sparkwingruntime.LookupClosure(planned.Plan, req.Closure)
	if !ok {
		return nil, &nodeproto.Error{Code: nodeproto.CodeUnknownClosure, Message: fmt.Sprintf("closure %q is not in this process's plan", req.Closure)}
	}
	if c.Kind == sparkwingruntime.ClosureGenerator {
		return nil, &nodeproto.Error{Message: "this SDK does not evaluate generator closures yet"}
	}
	var nodeErr error
	if c.Kind == sparkwingruntime.ClosureAfterRun {
		var in afterRunInput
		if err := json.Unmarshal(req.Input, &in); err != nil || (in.Outcome != nodeproto.OutcomeSuccess && in.Outcome != nodeproto.OutcomeFailed) {
			return nil, &nodeproto.Error{Code: nodeproto.CodeBadRequest, Message: "after_run input needs an outcome of success or failed"}
		}
		if in.Outcome == nodeproto.OutcomeFailed {
			nodeErr = errors.New(in.Error)
		}
	}
	ctx = s.nodeContext(ctx, planned, c.Node)
	if c.Step != nil {
		ctx = sparkwing.WithStep(ctx, c.Step.ID())
	}
	value, err := callClosure(ctx, c, nodeErr)
	var res nodeproto.EvalResult
	if err != nil {
		msg := err.Error()
		res.Error = &msg
		value = nil
	}
	if res.Value, err = json.Marshal(value); err != nil {
		return nil, err
	}
	return res, nil
}

// safety: a panic in a closure is the closure failing, so it answers error
// in a successful reply, the same as a returned error.
func callClosure(ctx context.Context, c sparkwingruntime.Closure, nodeErr error) (value any, err error) {
	defer func() {
		if p := recover(); p != nil {
			value, err = nil, fmt.Errorf("%s closure of %q panicked: %v", c.Kind, c.Node.ID(), p)
		}
	}()
	switch fn := c.Fn.(type) {
	case sparkwing.SkipPredicate:
		return fn(ctx), nil
	case sparkwing.CacheKeyFn:
		key, err := fn(ctx)
		if err != nil || key.IsNoCache() {
			return nil, err
		}
		return string(key), nil
	case sparkwing.BeforeRunFn:
		return nil, fn(ctx)
	case sparkwing.VerifyFn:
		return nil, fn(ctx)
	case sparkwing.AfterRunFn:
		fn(ctx, nodeErr)
		return nil, nil
	}
	return nil, fmt.Errorf("%s closure of %q holds a %T", c.Kind, c.Node.ID(), c.Fn)
}
