package nodehost

import (
	"context"
	"errors"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (h *OwnerEvaluator) evaluateProcess(ctx context.Context, input EvaluationInput, spec runspec.Spec, evaluate func(*executorSession, NodeInput) error) (err error) {
	var grant ProcessGrant
	var process ExecutorProcess
	defer func() {
		if grant.Revoke != nil {
			err = errors.Join(err, grant.Revoke())
		}
		if process != nil {
			closeErr := process.Close()
			err = errors.Join(err, closeErr, process.Cleanup(lease.ExecutionContext(ctx)))
		}
		if grant.masks != nil {
			err = maskHostError(err, grant.masks.masker)
		}
	}()
	resolved, err := h.deps.EnsureEngine(ctx, spec.Resolved.Engine, input.Engine.Target)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(resolved.Identity, spec.Resolved.Engine) {
		return executorError(apierr.SpecDrift, "engine resolution changed the sealed release")
	}
	input.Engine = resolved
	if h.deps.Bind != nil {
		grant, err = h.deps.Bind(ctx, input)
		if err != nil {
			return err
		}
		if err := grant.validate(input.RouteBinding); err != nil {
			return err
		}
		input.RouteBinding = grant.Binding
		input.processRevoke = grant.Revoke
	} else if input.RouteBinding.Routes.Bearer != "" {
		return errors.New("SDK evaluation requires a fresh process binding factory")
	}
	process, err = h.deps.Start(ctx, input)
	if err != nil {
		return err
	}
	if process == nil {
		return errors.New("SDK evaluator returned no process")
	}
	session := &executorSession{process: process, masks: grant.masks}
	code := NodeInput{Storage: input.Storage, Engine: input.Engine, Accepted: input.Accepted, NodeID: input.NodeID, SpecHash: input.SpecHash, GrantedRunner: input.GrantedRunner, RouteBinding: input.RouteBinding, Replay: input.Replay}
	if err = h.hello(ctx, session, code); err != nil {
		return err
	}
	if err = h.replan(ctx, session, code, spec); err != nil {
		return err
	}
	return evaluate(session, code)
}
