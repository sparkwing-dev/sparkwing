package nodehost

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const (
	defaultSkipBudget    = 30 * time.Second
	afterRunTimeoutLimit = 2 * time.Minute
)

// skipEvaluation records the guard result and indices of timed-out predicates.
type skipEvaluation struct {
	Skip     bool
	TimedOut []int
}

// predicateEvaluator shares budgets and result validation across job and approval guards.
type predicateEvaluator struct {
	code         *sdkCode
	childContext func(context.Context, time.Duration, time.Duration) (context.Context, context.CancelFunc, error)
	process      func(context.Context, func(*executorSession, NodeInput) error) error
}

func (h predicateEvaluator) skipIf(ctx context.Context, job nodeproto.JobSpec) (skipEvaluation, error) {
	var err error
	budget := defaultSkipBudget
	if job.SkipIfBudgetMS != nil {
		if *job.SkipIfBudgetMS <= 0 || *job.SkipIfBudgetMS > math.MaxInt64/int64(time.Millisecond) {
			return skipEvaluation{}, executorError(apierr.PlanInvalid, "skip predicate budget must be a positive duration")
		}
		budget = time.Duration(*job.SkipIfBudgetMS) * time.Millisecond
	}
	result := skipEvaluation{}
	for i, id := range job.Closures.SkipIf {
		err = h.process(ctx, func(session *executorSession, code NodeInput) error {
			bounded, cancel, err := h.childContext(ctx, budget, 0)
			if err != nil {
				return err
			}
			defer cancel()
			value, evalErr := h.code.eval(bounded, session, code, id, "skip_if", nil, nil)
			if ctx.Err() == nil && errors.Is(context.Cause(bounded), context.DeadlineExceeded) {
				result.TimedOut = append(result.TimedOut, i)
				return nil
			}
			if evalErr != nil {
				return evalErr
			}
			value = bytes.TrimSpace(value)
			if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
				return executorError(apierr.ProtocolError, "skip predicate must return a boolean")
			}
			result.Skip = bytes.Equal(value, []byte("true"))
			return nil
		})
		if err = errors.Join(err, context.Cause(ctx)); err != nil {
			return skipEvaluation{}, err
		}
		if result.Skip {
			break
		}
	}
	return result, nil
}

func (h *nodeExecutor) skipIf(ctx context.Context, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec) (skipEvaluation, error) {
	evaluator := predicateEvaluator{code: &h.sdkCode, childContext: h.deps.ChildContext, process: func(ctx context.Context, evaluate func(*executorSession, NodeInput) error) error {
		return h.withProcess(ctx, input, spec, evaluate)
	}}
	result, err := evaluator.skipIf(ctx, job)
	return result, maskHostError(err, h.deps.Masker)
}

func (h *nodeExecutor) withProcess(ctx context.Context, input NodeInput, spec runspec.Spec, evaluate func(*executorSession, NodeInput) error) (err error) {
	var grant ProcessGrant
	var process ExecutorProcess
	defer func() {
		if grant.Revoke != nil {
			err = errors.Join(err, grant.Revoke())
		}
		if process != nil {
			err = errors.Join(err, process.Close(), process.Cleanup(lease.ExecutionContext(ctx)))
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
		input.RouteBinding, input.processRevoke = grant.Binding, grant.Revoke
	} else if input.RouteBinding.Routes.Bearer != "" {
		return errors.New("SDK evaluation requires a fresh process binding factory")
	}
	process, err = h.deps.Start(ctx, input, ExecutorActivity{})
	if err != nil {
		return err
	}
	if process == nil {
		return errors.New("SDK evaluator returned no process")
	}
	session := &executorSession{process: process}
	if err := h.hello(ctx, session, input); err != nil {
		return err
	}
	if err := h.replan(ctx, session, input, spec); err != nil {
		return err
	}
	return evaluate(session, input)
}
