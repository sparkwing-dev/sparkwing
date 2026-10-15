package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (h *nodeExecutor) attempt(ctx context.Context, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec, first, retryAllowed bool) (result store.NodeResult, err error) {
	parent := ctx
	started := !first
	ctx, cancel, err := h.deps.ChildContext(ctx, input.Execution.Timeout.Value, 0)
	if err != nil {
		return h.failure(err), err
	}
	ctx, activity, stopIdle := executorProgressContext(ctx, input.Execution.NoProgressTimeout.Value)
	var process ExecutorProcess
	var session *executorSession
	afterRun := false
	defer func() {
		timeout := executorAttemptTimeout(ctx)
		stopIdle()
		cancel()
		if timeout != nil {
			result = h.failure(timeout)
			var cleanupErr *executorCleanupError
			if errors.As(err, &cleanupErr) {
				result = h.failure(errors.Join(timeout, cleanupErr))
			} else if started {
				err = nil
			}
			if started && !retryAllowed && !afterRun && session != nil && parent.Err() == nil {
				afterCtx, afterCancel := context.WithTimeout(parent, limits.CleanupTimeout)
				afterErr := h.afterRun(afterCtx, session, input, job, result)
				afterCancel()
				if afterErr != nil {
					err = errors.Join(err, afterErr)
					result = h.failure(errors.Join(timeout, err))
				}
			}
		}
		if process != nil {
			closeErr := process.Close()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), limits.CleanupTimeout)
			cleanupErr := process.Cleanup(cleanupCtx)
			cleanupCancel()
			if cleanupFailure := errors.Join(closeErr, cleanupErr); cleanupFailure != nil {
				err = errors.Join(err, cleanupFailure)
				result = h.failure(err)
			}
		}
	}()
	resolved, err := h.deps.EnsureEngine(ctx, spec.Resolved.Engine, input.Engine.Target)
	if err != nil {
		return h.failure(err), err
	}
	if !reflect.DeepEqual(resolved.Identity, spec.Resolved.Engine) {
		err = executorError(apierr.SpecDrift, "engine resolution changed the sealed release")
		return h.failure(err), err
	}
	input.Engine = resolved
	if h.deps.Bind != nil {
		binding, bindErr := h.deps.Bind(ctx, input)
		if bindErr != nil {
			return h.failure(bindErr), bindErr
		}
		if err := checkProcessBinding(input.RouteBinding, binding); err != nil {
			return h.failure(err), err
		}
		input.RouteBinding = binding
	} else if input.RouteBinding.Routes.Bearer != "" {
		err = errors.New("SDK execution requires a fresh process binding factory")
		return h.failure(err), err
	}
	process, err = h.deps.Start(ctx, input, activity)
	if err != nil {
		return h.failure(err), err
	}
	if process == nil {
		err = errors.New("SDK process adapter returned no process")
		return h.failure(err), err
	}
	session = &executorSession{process: process}
	if err = h.hello(ctx, session, input); err != nil {
		return h.failure(err), err
	}
	if err = h.replan(ctx, session, input, spec); err != nil {
		return h.failure(err), err
	}
	if job.OnFailureOf != "" {
		if h.deps.ReadFailure == nil {
			err = errors.New("recovery execution requires the persisted source failure")
			return h.failure(err), err
		}
		source := store.NodeRef{RunID: input.Accepted.RunID, NodeID: job.OnFailureOf}
		row, readErr := h.deps.Store.ReadNode(ctx, source)
		if readErr != nil {
			return h.failure(readErr), readErr
		}
		if row.Ref != source || row.Status != store.StatusDone || row.Outcome != store.NodeFailed {
			err = executorError(apierr.SpecDrift, "recovery source is not a failed job")
			return h.failure(err), err
		}
		failure, readErr := h.deps.ReadFailure(ctx, source)
		if readErr != nil {
			return h.failure(readErr), readErr
		}
		if failure.Job != job.OnFailureOf || failure.Error == nil {
			err = executorError(apierr.SpecDrift, "recovery failure does not match its source")
			return h.failure(err), err
		}
		session.failure = &failure
	}
	if first {
		applied, startErr := h.transition(ctx, input, store.CauseStart, store.NodeRunning, store.TransitionExtra{Attempt: &store.NodeAttempt{Identity: runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: input.Attempt, Generation: input.Generation}, Holder: input.Authority.Holder, CapabilityPrefix: input.Authority.CapabilityPrefix, ExecutorName: input.GrantedRunner.Name, ExecutorKind: input.GrantedRunner.Type}})
		if startErr != nil {
			return h.failure(startErr), startErr
		}
		if applied.Generation != input.Generation || applied.Attempt != input.Attempt {
			err = errors.New("start transition returned an inconsistent claim or attempt")
			return h.failure(err), err
		}
		started = true
	}
	if input.Attempt == 1 {
		for _, closure := range job.Closures.BeforeRun {
			if _, err = h.eval(ctx, session, input, closure, nil); err != nil {
				return h.failure(err), err
			}
		}
	}
	output, bodyErr := h.steps(ctx, parent, session, input, spec, job)
	var cleanupErr *executorCleanupError
	if errors.As(bodyErr, &cleanupErr) {
		err = cleanupErr
	}
	timeout := executorAttemptTimeout(ctx)
	result = store.NodeResult{Outcome: store.NodeSuccess}
	if bodyErr != nil {
		result = h.failure(bodyErr)
		var application *executorApplicationError
		if !errors.As(bodyErr, &application) && timeout == nil {
			return result, bodyErr
		}
	}
	if timeout != nil {
		result = h.failure(timeout)
	}
	if bodyErr == nil && timeout == nil && job.Closures.Verify != "" {
		if _, verifyErr := h.eval(ctx, session, input, job.Closures.Verify, nil); verifyErr != nil {
			result = h.failure(verifyErr)
		}
	}
	if executorAttemptTimeout(ctx) == nil && (result.Outcome != store.NodeFailed || !retryAllowed) {
		afterRun = true
		if afterErr := h.afterRun(ctx, session, input, job, result); afterErr != nil {
			return h.failure(afterErr), afterErr
		}
	}
	if result.Outcome == store.NodeSuccess && len(output) > 0 {
		if h.deps.PublishOutput == nil {
			err = errors.New("job output requires a record publisher")
			return h.failure(err), err
		}
		result.OutputRecord, err = h.deps.PublishOutput(ctx, input, output)
		if err != nil {
			return h.failure(err), err
		}
		if result.OutputRecord == nil {
			err = errors.New("output publisher returned no verified record")
			return h.failure(err), err
		}
	}
	return result, err
}

func (h *nodeExecutor) afterRun(ctx context.Context, session *executorSession, input NodeInput, job nodeproto.JobSpec, result store.NodeResult) error {
	after := nodeproto.RunResult{Outcome: string(result.Outcome)}
	if result.Outcome == store.NodeFailed {
		code := apierr.SDKError
		if result.Reason == string(apierr.DeadlineExceeded) {
			code = apierr.DeadlineExceeded
		}
		after.Error = &apierr.Error{Code: code, Message: result.MaskedError}
	}
	payload, err := json.Marshal(after)
	if err != nil {
		return err
	}
	for _, closure := range job.Closures.AfterRun {
		if _, err := h.eval(ctx, session, input, closure, payload); err != nil {
			return err
		}
	}
	return nil
}

func executorContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func executorResultFailure(result nodeproto.RunResult) error {
	if result.Outcome == "success" && result.Error == nil {
		return nil
	}
	if result.Outcome == "failed" {
		if result.Error != nil {
			return &executorApplicationError{err: result.Error}
		}
		return &executorApplicationError{err: executorError(apierr.SDKError, "job body failed")}
	}
	return executorError(apierr.ProtocolError, fmt.Sprintf("SDK returned invalid outcome %q", result.Outcome))
}

type executorApplicationError struct{ err error }

func (e *executorApplicationError) Error() string { return e.err.Error() }
func (e *executorApplicationError) Unwrap() error { return e.err }
