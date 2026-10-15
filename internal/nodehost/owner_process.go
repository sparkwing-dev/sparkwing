package nodehost

import (
	"context"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (h *OwnerEvaluator) withProcess(ctx context.Context, input EvaluationInput, spec runspec.Spec, evaluate func(*executorSession, NodeInput) error) (err error) {
	resolved, err := h.deps.EnsureEngine(ctx, spec.Resolved.Engine, input.Engine.Target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(resolved.Identity, spec.Resolved.Engine) {
		return executorError(apierr.SpecDrift, "engine resolution changed the sealed release")
	}
	input.Engine = resolved
	if h.deps.Bind != nil {
		binding, bindErr := h.deps.Bind(ctx, input)
		if bindErr != nil {
			return bindErr
		}
		if err := checkProcessBinding(input.RouteBinding, binding); err != nil {
			return err
		}
		input.RouteBinding = binding
	} else if input.RouteBinding.Routes.Bearer != "" {
		return errors.New("SDK evaluation requires a fresh process binding factory")
	}
	process, err := h.deps.Start(ctx, input)
	if process != nil {
		defer func() {
			closeErr := process.Close()
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.CleanupTimeout)
			defer cancel()
			err = errors.Join(err, closeErr, process.Cleanup(cleanupCtx))
		}()
	}
	if err != nil {
		return err
	}
	if process == nil {
		return errors.New("SDK evaluator returned no process")
	}
	session := &executorSession{process: process}
	code := NodeInput{Engine: input.Engine, Accepted: input.Accepted, NodeID: input.NodeID, SpecHash: input.SpecHash, GrantedRunner: input.GrantedRunner, RouteBinding: input.RouteBinding, Replay: input.Replay}
	if err = h.hello(ctx, session, code); err != nil {
		return err
	}
	if err = h.replan(ctx, session, code, spec); err != nil {
		return err
	}
	return evaluate(session, code)
}
