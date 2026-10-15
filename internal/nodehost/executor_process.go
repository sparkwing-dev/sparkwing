package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type executorSession struct {
	process  ExecutorProcess
	sequence atomic.Int64
	failure  *nodeproto.JobFailure
}

func (s *executorSession) call(ctx context.Context, request nodeproto.Request, result any) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	request.ID = strconv.FormatInt(s.sequence.Add(1), 10)
	if request.Op == nodeproto.OpRunStep || request.Op == nodeproto.OpEval {
		request.Failure = s.failure
	}
	reply, err := s.process.Request(ctx, request)
	if err != nil {
		return err
	}
	if reply.ID != request.ID || reply.OK != (reply.Error == nil) || reply.OK == (len(reply.Result) == 0) || reply.Error != nil && len(reply.Result) != 0 {
		return executorError(apierr.ProtocolError, "SDK returned an invalid reply")
	}
	if reply.Error != nil {
		return reply.Error
	}
	if err = json.Unmarshal(reply.Result, result); err != nil {
		return executorError(apierr.ProtocolError, "SDK reply has an invalid result")
	}
	return nil
}

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

func (h *nodeExecutor) hello(ctx context.Context, session *executorSession, input NodeInput) error {
	var hello nodeproto.HelloResult
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: input.Engine.Identity.Version}, Runner: &input.GrantedRunner, Binding: &input.RouteBinding}, &hello)
	if err != nil {
		return err
	}
	if hello.Protocol != nodeproto.Version || hello.SDK.Language != string(input.Engine.Identity.Pin.SDK) || strings.TrimPrefix(hello.SDK.Version, "v") != strings.TrimPrefix(input.Engine.Identity.Pin.Version, "v") {
		return executorError(apierr.ProtocolMismatch, "SDK hello does not match the sealed engine and SDK pin")
	}
	return nil
}

func (h *nodeExecutor) replan(ctx context.Context, session *executorSession, input NodeInput, spec runspec.Spec) error {
	run := executorRunContext(spec)
	run.RunID = input.Accepted.RunID
	args := spec.Resolved.Args
	if args == nil {
		args = map[string]string{}
	}
	var raw json.RawMessage
	err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: spec.Request.Request.Pipeline, Args: args, Run: &run}, &raw)
	if err != nil {
		return err
	}
	plan, err := h.deps.ValidatePlan(raw, h.deps.Validation)
	if err != nil {
		return err
	}
	hash, err := h.deps.CanonicalHash(plan)
	if err != nil {
		return err
	}
	if string(hash) != input.Accepted.PlanHash {
		return executorError(apierr.PlanDrift, "SDK plan differs from the accepted plan")
	}
	jobs := append([]nodeproto.JobSpec(nil), plan.Jobs...)
	var snapshot store.RunSnapshot
	if len(input.Replay) > 0 {
		snapshot, err = h.deps.Store.ReadSnapshot(ctx, input.Accepted.RunID)
		if err != nil {
			return err
		}
		if snapshot.Run.ID != input.Accepted.RunID {
			return executorError(apierr.PlanDrift, "generator snapshot belongs to another run")
		}
	}
	for _, replay := range input.Replay {
		if replay.SourceGeneration < 1 || replay.SourceJob == "" || replay.GeneratorID == "" {
			return executorError(apierr.PlanDrift, "generator replay lacks its persisted source identity")
		}
		var sourceJob *nodeproto.JobSpec
		for i := range jobs {
			if jobs[i].ID == replay.SourceJob {
				sourceJob = &jobs[i]
				break
			}
		}
		if sourceJob == nil || !executorContains(sourceJob.Closures.Generate, replay.GeneratorID) {
			return executorError(apierr.PlanDrift, "generator replay names an undeclared source")
		}
		replayInput := input
		replayInput.NodeID = replay.SourceJob
		value, evalErr := h.eval(ctx, session, replayInput, replay.GeneratorID, replay.Output)
		if evalErr != nil {
			return evalErr
		}
		validation := h.deps.Validation
		validation.Expansion = true
		validation.ExistingJobs = jobs
		validation.SourceJob = replay.SourceJob
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '[' {
			return executorError(apierr.ProtocolError, "generator replay must return a jobs array")
		}
		wrapped := append([]byte(`{"jobs":`), value...)
		wrapped = append(wrapped, '}')
		expansion, validateErr := h.deps.ValidatePlan(wrapped, validation)
		if validateErr != nil {
			return validateErr
		}
		expected, expectedErr := h.replayMembers(snapshot, replay)
		if expectedErr != nil {
			return expectedErr
		}
		if len(expansion.Jobs) != len(expected) {
			return executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
		}
		for _, generated := range expansion.Jobs {
			for _, existing := range jobs {
				if existing.ID == generated.ID {
					return executorError(apierr.PlanDrift, "generator replay overwrote an existing job")
				}
			}
			if !expected[generated.ID] || !executorContains(generated.Groups, replay.GeneratorID) {
				return executorError(apierr.PlanDrift, "generator replay changed the accepted member set")
			}
			row, readErr := h.deps.Store.ReadNode(ctx, store.NodeRef{RunID: input.Accepted.RunID, NodeID: generated.ID})
			if readErr != nil {
				return executorError(apierr.PlanDrift, "generator replay added a job absent from the accepted expansion")
			}
			hash, hashErr := h.deps.CanonicalHash(generated)
			if hashErr != nil {
				return hashErr
			}
			if string(hash) != row.SpecHash {
				return executorError(apierr.PlanDrift, "generator replay changed a persisted job")
			}
			if row.Ref.RunID != input.Accepted.RunID || row.Ref.NodeID != generated.ID {
				return executorError(apierr.PlanDrift, "generated job row belongs to another identity")
			}
			delete(expected, generated.ID)
			jobs = append(jobs, generated)
		}
	}
	for _, job := range jobs {
		if job.ID != input.NodeID {
			continue
		}
		hash, err := h.deps.CanonicalHash(job)
		if err != nil {
			return err
		}
		if string(hash) != input.SpecHash {
			return executorError(apierr.PlanDrift, "SDK job differs from the accepted job")
		}
		return nil
	}
	return executorError(apierr.PlanDrift, "SDK did not reconstruct the accepted job")
}

func (h *nodeExecutor) replayMembers(snapshot store.RunSnapshot, replay GeneratorReplay) (map[string]bool, error) {
	for _, accepted := range snapshot.Expansions {
		if accepted.Source.RunID != snapshot.Run.ID || accepted.Source.NodeID != replay.SourceJob || accepted.SourceGeneration != replay.SourceGeneration {
			continue
		}
		var plan nodeproto.PlanDoc
		if err := json.Unmarshal(accepted.Body, &plan); err != nil {
			return nil, executorError(apierr.PlanDrift, "accepted expansion is malformed")
		}
		hash, err := h.deps.CanonicalHash(plan)
		if err != nil {
			return nil, err
		}
		if string(hash) != accepted.BodyHash {
			return nil, executorError(apierr.PlanDrift, "accepted expansion hash differs from its body")
		}
		members := map[string]bool{}
		for _, job := range plan.Jobs {
			if executorContains(job.Groups, replay.GeneratorID) {
				members[job.ID] = true
			}
		}
		return members, nil
	}
	return nil, executorError(apierr.PlanDrift, "generator replay has no accepted expansion for its source generation")
}

func (h *nodeExecutor) eval(ctx context.Context, session *executorSession, input NodeInput, closure string, payload json.RawMessage) (json.RawMessage, error) {
	var result nodeproto.EvalResult
	if err := session.call(ctx, nodeproto.Request{Op: nodeproto.OpEval, Job: input.NodeID, Closure: closure, Input: payload, Attempt: int(input.Attempt)}, &result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, result.Error
	}
	if !json.Valid(result.Value) {
		return nil, executorError(apierr.ProtocolError, "SDK closure returned invalid JSON")
	}
	return result.Value, nil
}

func executorRunContext(spec runspec.Spec) nodeproto.RunContext {
	request := spec.Request.Request
	git := request.Git
	context := nodeproto.RunContext{RunID: spec.Request.RunID, Pipeline: request.Pipeline, StartedAt: spec.Request.AdmittedAt, NoMemo: spec.Resolved.Settings.Options.NoMemo.Value, DryRun: spec.Resolved.Settings.Options.DryRun.Value, Git: &nodeproto.Git{SHA: git.SHA, Branch: git.Ref, DefaultBranch: spec.Resolved.DefaultBranch.Value, Repo: git.Repository.Display}, Trigger: &nodeproto.Trigger{Source: string(request.Origin), User: request.Actor.Display}}
	if request.PullRequest.Set {
		pr := request.PullRequest.Value
		context.Trigger.PullRequest = &nodeproto.PullRequest{Number: int(pr.Number), BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA}
	}
	return context
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
