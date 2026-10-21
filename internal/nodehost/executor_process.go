package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (h *nodeExecutor) attempt(ctx context.Context, input NodeInput, spec runspec.Spec, job nodeproto.JobSpec, retryAllowed bool) (result store.NodeResult, err error) {
	parent := ctx
	ctx, cancel, err := h.deps.ChildContext(ctx, input.Execution.Timeout.Value, 0)
	if err != nil {
		return h.failure(err), err
	}
	ctx, activity, stopIdle := executorProgressContext(ctx, input.Execution.NoProgressTimeout.Value)
	var process ExecutorProcess
	var grant ProcessGrant
	var session *executorSession
	afterRun := false
	stage := "action"
	defer func() {
		timeout := executorAttemptTimeout(ctx)
		stopIdle()
		cancel()
		if timeout != nil {
			result = h.failureAt(timeout, stage)
			var cleanupErr *executorCleanupError
			var writeErr *executorStepRecordError
			writeFailed := errors.As(err, &writeErr)
			if errors.As(err, &cleanupErr) || writeFailed {
				result = h.failureAt(errors.Join(timeout, err), stage)
			} else {
				err = nil
			}
			if !retryAllowed && !afterRun && session != nil && parent.Err() == nil && !writeFailed {
				afterCtx, afterCancel := context.WithTimeout(parent, limits.CleanupTimeout)
				afterErr := h.afterRun(afterCtx, session, input, job, result)
				afterCancel()
				if afterErr != nil {
					err = errors.Join(err, afterErr)
					result = h.failure(errors.Join(timeout, err))
				}
			}
		}
		if grant.Revoke != nil {
			if revokeErr := grant.Revoke(); revokeErr != nil {
				err = errors.Join(err, revokeErr)
				result = h.failure(err)
			}
		}
		if process != nil {
			closeErr := process.Close()
			cleanupErr := process.Cleanup(lease.ExecutionContext(parent))
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
		grant, err = h.deps.Bind(ctx, input)
		if err != nil {
			return h.failure(err), err
		}
		if err := grant.validate(input.RouteBinding); err != nil {
			return h.failure(err), err
		}
		input.RouteBinding = grant.Binding
		input.processRevoke = grant.Revoke
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
		source := controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: job.OnFailureOf}
		row, readErr := h.deps.Store.ReadNode(ctx, source)
		if readErr != nil {
			return h.failure(readErr), readErr
		}
		if row.Ref != source || row.Status != store.StatusDone || row.Outcome != store.NodeFailed {
			err = executorError(apierr.SpecDrift, "recovery source is not a failed job")
			return h.failure(err), err
		}
		failure, readErr := h.deps.ReadFailure(ctx, source, int64(row.Generation))
		if readErr != nil {
			return h.failure(readErr), readErr
		}
		if failure == nil || failure.Job != job.OnFailureOf || failure.Error == nil {
			err = executorError(apierr.SpecDrift, "recovery failure does not match its source")
			return h.failure(err), err
		}
		session.failure = failure
	}
	if input.Attempt == 1 {
		for _, closure := range job.Closures.BeforeRun {
			if _, err = h.eval(ctx, session, input, closure, "before_run", nil, nil); err != nil {
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
		var recordFailure *executorStepRecordError
		if errors.As(bodyErr, &recordFailure) {
			return result, bodyErr
		}
		var application *executorApplicationError
		if !errors.As(bodyErr, &application) && timeout == nil {
			return result, bodyErr
		}
	}
	if timeout != nil {
		result = h.failure(timeout)
	}
	if bodyErr == nil && timeout == nil && job.Closures.Verify != "" {
		stage = "verify"
		if _, verifyErr := h.eval(ctx, session, input, job.Closures.Verify, "verify", nil, nil); verifyErr != nil {
			result = h.failureAt(verifyErr, stage)
		}
	}
	if executorAttemptTimeout(ctx) == nil && (result.Outcome != store.NodeFailed || !retryAllowed) {
		stage = "action"
		afterRun = true
		if afterErr := h.afterRun(ctx, session, input, job, result); afterErr != nil {
			return h.failure(afterErr), afterErr
		}
	}
	if result.Outcome == store.NodeSuccess && len(output) > 0 && h.deps.Mask(string(output)) != string(output) {
		output = nil
		if err = h.withholdOutput(ctx, input); err != nil {
			return h.failure(err), err
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
		if _, err := h.eval(ctx, session, input, closure, "after_run", payload, nil); err != nil {
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

const outputWithheld = "output withheld: contains a secret"

func (h *nodeExecutor) withholdOutput(ctx context.Context, input NodeInput) error {
	attrs := map[string]any{}
	if h.deps.Annotate == nil {
		attrs["annotation_error"] = "annotation persistence is unavailable"
	} else if err := h.deps.Annotate(ctx, input, nodeproto.AnnotationRequest{Level: "warn", Message: outputWithheld}); err != nil {
		attrs["annotation_error"] = err.Error()
	}
	return h.diagnostic(ctx, input, "warn", "output_withheld", "", outputWithheld, attrs)
}
