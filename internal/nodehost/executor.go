package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NodeTransitionStore is the executor's fenced state boundary.
type NodeTransitionStore interface {
	ReadNode(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	TransitionNode(context.Context, store.NodeCommand) (store.Applied, error)
	RecordStep(context.Context, store.StepReport) error
}

// ExecutorProcess must cancel pending requests when their context ends while
// remaining available for bounded timeout cleanup. Close
// revokes the process binding and joins the process; Cleanup then runs registered
// cleanups under its own bounded context. Both are called after failed setup too.
type ExecutorProcess interface {
	Request(context.Context, nodeproto.Request) (nodeproto.Reply, error)
	Close() error
	Cleanup(context.Context) error
}

// ExecutorFinalizer saves an executor layer's state after its inner layers end.
// Finalizers receive the last committed attempt and its refreshed storage binding.
// Artifact publication returns verified references; the store authorizes attachment.
type ExecutorFinalizer func(context.Context, NodeInput, *store.NodeResult) error

// ExecutorActivity connects logs and authorized child waits to the idle budget.
// Await pauses that budget until its returned function is called. Concurrent
// waits nest, and each returned function may safely be called more than once.
type ExecutorActivity struct {
	Progress func()
	Await    func() (resume func())
}

// ExecutorDependencies supplies process and storage adapters without changing
// the accepted execution settings. Start receives the granted hello identity and
// activity callbacks for the no-progress budget. Its context bounds startup;
// Close owns the process lifetime. Execute's context must belong
// to the claim's lease keeper, whose lost-authority cause suppresses reporting.
// Bind delegates a fresh process bearer for each attempt; the host revokes it before joining and cleanup.
// A bearer-bearing input requires Bind; unbound transports may omit it.
// BindStorage refreshes byte/record authority after each start or retry commit.
// Configured storage requires this factory; failed refresh suppresses finalizer writes.
// Log receives masked host diagnostics with their committed attempt; delivery errors stop work.
type ExecutorDependencies struct {
	memoEnvironment  func(context.Context, NodeInput) (procenv.Env, error)
	Log              func(context.Context, NodeInput, nodeproto.FrameLog) error
	BindStorage      func(context.Context, NodeInput) (StorageBinding, error)
	Store            NodeTransitionStore
	Bind             func(context.Context, NodeInput) (ProcessGrant, error)
	Start            func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error)
	EnsureEngine     func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
	ChildContext     func(context.Context, time.Duration, time.Duration) (context.Context, context.CancelFunc, error)
	Validation       nodeproto.ValidationContext
	PrepareArtifacts func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error)
	PrepareCaches    func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error)
	PublishOutput    func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error)
	Annotate         func(context.Context, NodeInput, nodeproto.AnnotationRequest) error
	ReadFailure      func(context.Context, controllerwire.NodeRef, int64) (*nodeproto.JobFailure, error)
	ReadOutput       func(context.Context, controllerwire.NodeRef, int64) (json.RawMessage, error)
	Masker           *secrets.Masker
	Masks            *MaskRegistry
}

type nodeExecutor struct {
	deps ExecutorDependencies
	sdkCode
}

// NewNodeExecutor builds the common executor for local and dispatched claims.
// Validation, hashing and deadlines are owned by their contract packages.
func NewNodeExecutor(deps ExecutorDependencies) (NodeHost, error) {
	if deps.Store == nil || deps.Start == nil || deps.EnsureEngine == nil || deps.Masker == nil {
		return nil, errors.New("node executor requires store, process, engine and masking adapters")
	}
	if deps.Masks == nil {
		deps.Masks = NewMaskRegistry(deps.Masker)
	}
	if deps.Masks.masker != deps.Masker {
		return nil, errors.New("host requires one shared masking registry")
	}
	if deps.ChildContext == nil {
		deps.ChildContext = limits.ChildContext
	}
	return &nodeExecutor{deps: deps, sdkCode: sdkCode{Store: deps.Store, ReadOutput: deps.ReadOutput, Validation: deps.Validation}}, nil
}

func (h *nodeExecutor) Execute(ctx context.Context, input NodeInput) (err error) {
	defer func() {
		err = maskHostError(err, h.deps.Masker)
	}()
	if err := h.deps.Masks.initializeSeal(input.Accepted); err != nil {
		return err
	}
	if err := executorLostAuthority(ctx); err != nil {
		return err
	}
	if err := h.deps.Masks.initializeAccepted(ctx, h.deps.Store, input.Accepted); err != nil {
		return err
	}
	spec, row, job, err := h.accepted(ctx, input)
	if err != nil {
		var drift *apierr.Error
		if errors.As(err, &drift) && (drift.Code == apierr.PlanDrift || drift.Code == apierr.SpecDrift) {
			return h.report(ctx, input, h.failure(err), err)
		}
		return err
	}
	if ctx.Err() != nil {
		return h.report(ctx, input, h.failure(context.Cause(ctx)), context.Cause(ctx))
	}
	if row.CancelRequestedAt != nil {
		return h.report(ctx, input, store.NodeResult{Outcome: store.NodeCancelled, Reason: string(apierr.ReasonCancelled)}, nil)
	}
	input, err = h.startExecution(ctx, input, spec)
	if err != nil {
		return h.report(ctx, input, h.failure(err), err)
	}
	input, err = h.bindStorage(ctx, input)
	if err != nil {
		return h.report(ctx, input, h.failure(err), err)
	}
	decision, err := h.skipIf(ctx, input, spec, job)
	if err != nil {
		return h.report(ctx, input, h.failure(err), err)
	}
	for _, index := range decision.TimedOut {
		if err := h.diagnostic(ctx, input, "warn", "skip_if_timeout", "", "SkipIf predicate exceeded its budget; continuing without skipping", map[string]any{"predicate": index}); err != nil {
			return h.report(ctx, input, h.failure(err), err)
		}
	}
	if decision.Skip {
		return h.report(ctx, input, store.NodeResult{Outcome: store.NodeSkipped, Reason: "skip_if"}, nil)
	}
	var memoHash runspec.Hash
	var memoResult *store.NodeResult
	if job.Memo != nil || job.Closures.MemoKey != "" {
		err = h.withProcess(ctx, input, spec, func(session *executorSession, code NodeInput) error {
			memoHash, memoResult, err = h.memo(ctx, session, code, job, spec)
			return err
		})
		if err != nil {
			return h.report(ctx, input, h.failure(err), err)
		}
		if memoResult != nil {
			return h.report(ctx, input, *memoResult, nil)
		}
	}
	result, runErr := h.layers(ctx, &input, job, func() (store.NodeResult, error) { return h.attempts(ctx, &input, spec, row, job) })
	if runErr == nil && result.Outcome == store.NodeSuccess {
		if err := h.saveMemo(ctx, input, memoHash, result); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				runErr, result = errors.Join(cause, err), h.failure(errors.Join(cause, err))
			} else if logErr := h.diagnostic(ctx, input, "warn", "memo_publish", "", "memo publication failed; a later run will execute again", map[string]any{"error": h.deps.Masker.Mask(err.Error())}); logErr != nil {
				runErr, result = logErr, h.failure(logErr)
			}
		}
	}
	return h.report(ctx, input, result, runErr)
}

func (h *nodeExecutor) accepted(ctx context.Context, input NodeInput) (runspec.Spec, store.NodeRow, nodeproto.JobSpec, error) {
	var spec runspec.Spec
	var row store.NodeRow
	var job nodeproto.JobSpec
	if input.Accepted.RunID == "" || input.NodeID == "" || input.Attempt < 0 || input.Generation < 1 || input.Authority.Holder == "" || input.Authority.CapabilityPrefix == "" || input.Entry == nil {
		return spec, row, job, errors.New("node execution lacks an accepted claim or source entry")
	}
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		return spec, row, job, executorError(apierr.SpecDrift, "accepted run spec is malformed")
	}
	if input.Accepted.SpecHash == "" || string(spec.Hash) != input.Accepted.SpecHash || string(spec.PlanHash) != input.Accepted.PlanHash {
		return spec, row, job, executorError(apierr.SpecDrift, "accepted run seal does not match execution")
	}
	if !reflect.DeepEqual(spec.Resolved.Engine, input.Engine.Identity) || !reflect.DeepEqual(input.Entry.Engine.Identity, input.Engine.Identity) {
		return spec, row, job, executorError(apierr.SpecDrift, "source and executor must use the sealed engine")
	}
	if input.RouteBinding.RunID != input.Accepted.RunID || input.RouteBinding.JobID != input.NodeID {
		return spec, row, job, executorError(apierr.SpecDrift, "process binding does not match the accepted job")
	}
	hash, err := runspec.CanonicalHash(input.Accepted.Plan)
	if err != nil {
		return spec, row, job, err
	}
	if string(hash) != input.Accepted.PlanHash {
		return spec, row, job, executorError(apierr.PlanDrift, "accepted plan hash does not match its declarations")
	}
	row, err = h.deps.Store.ReadNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)})
	if err != nil {
		return spec, row, job, err
	}
	if row.Ref.RunID != runspec.RunID(input.Accepted.RunID) || row.Ref.NodeID != runspec.NodeID(input.NodeID) || row.Generation != input.Generation || row.Attempt != input.Attempt || row.ClaimedBy != input.Authority.Holder || row.CapabilityPrefix != input.Authority.CapabilityPrefix {
		return spec, row, job, executorError(apierr.LockHeld, "node execution no longer holds the accepted claim")
	}
	constructed, err := NewNodeInput(input.Accepted, row, input.Engine, input.GrantedRunner)
	if err != nil {
		return spec, row, job, err
	}
	if constructed.SpecHash != input.SpecHash || !reflect.DeepEqual(constructed.Execution, input.Execution) || row.ApplicationRetries < 0 {
		return spec, row, job, executorError(apierr.SpecDrift, "node settings do not match the accepted job")
	}
	if input.Execution.Retry.Value < 0 || input.Execution.RetryBackoff.Value < 0 || input.Execution.Timeout.Value < 0 || input.Execution.NoProgressTimeout.Value < 0 {
		return spec, row, job, executorError(apierr.SpecDrift, "sealed execution settings contain a negative budget")
	}
	if err = json.Unmarshal(row.Spec, &job); err != nil || job.ID != input.NodeID {
		return spec, row, job, executorError(apierr.PlanDrift, "persisted job specification is invalid")
	}
	hash, err = runspec.CanonicalHash(job)
	if err != nil {
		return spec, row, job, err
	}
	if string(hash) != row.SpecHash {
		return spec, row, job, executorError(apierr.PlanDrift, "persisted job hash does not match its declarations")
	}
	return spec, row, job, nil
}

func (h *nodeExecutor) layers(ctx context.Context, input *NodeInput, job nodeproto.JobSpec, body func() (store.NodeResult, error)) (result store.NodeResult, err error) {
	var finalizers []ExecutorFinalizer
	defer func() {
		for i := len(finalizers) - 1; i >= 0; i-- {
			var refused *executorTransitionError
			if executorLostAuthority(ctx) != nil || errors.As(err, &refused) || h.deps.BindStorage != nil && input.Storage.Bytes == nil {
				break
			}
			if finishErr := finalizers[i](ctx, *input, &result); finishErr != nil {
				err = errors.Join(err, finishErr)
				result = h.failure(err)
			}
		}
	}()
	prepare := func(operation func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error), required bool) error {
		if operation == nil {
			if required {
				return errors.New("executor requires a declared storage adapter")
			}
			return nil
		}
		finish, err := operation(ctx, *input, job)
		if err != nil {
			return err
		}
		if finish != nil {
			finalizers = append(finalizers, finish)
		}
		return nil
	}
	if err := prepare(h.deps.PrepareArtifacts, len(job.Consumes) > 0 || len(job.Artifacts) > 0); err != nil {
		return h.failure(err), err
	}
	if err := prepare(h.deps.PrepareCaches, len(job.Caches) > 0); err != nil {
		return h.failure(err), err
	}
	return body()
}

func (h *nodeExecutor) startExecution(ctx context.Context, input NodeInput, spec runspec.Spec) (NodeInput, error) {
	if input.Attempt == math.MaxInt64 {
		return input, &executorTransitionError{err: errors.New("attempt ordinal exhausted")}
	}
	sourceAttempt := input.Attempt
	expected := sourceAttempt + 1
	applied, err := h.transition(ctx, input, store.StartNode{SourceAttempt: &sourceAttempt, Attempt: &store.NodeAttempt{
		Identity: runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: int64(expected), Generation: int64(input.Generation)},
		Holder:   input.Authority.Holder, CapabilityPrefix: input.Authority.CapabilityPrefix, ExecutorName: input.GrantedRunner.Name, ExecutorKind: input.GrantedRunner.Type,
	}})
	if err != nil {
		return input, err
	}
	if applied.Generation != input.Generation || applied.Attempt != expected {
		return input, &executorTransitionError{err: errors.New("start transition returned an inconsistent claim or attempt")}
	}
	input.Attempt = applied.Attempt
	return input, nil
}

func (h *nodeExecutor) attempts(ctx context.Context, input *NodeInput, spec runspec.Spec, row store.NodeRow, job nodeproto.JobSpec) (store.NodeResult, error) {
	remaining := max(0, input.Execution.Retry.Value-row.ApplicationRetries)
	for retry := 0; ; retry++ {
		result, err := h.attempt(ctx, *input, spec, job, retry < remaining)
		if ctx.Err() != nil {
			return h.failure(context.Cause(ctx)), context.Cause(ctx)
		}
		if err != nil || result.Outcome != store.NodeFailed || retry >= remaining {
			return result, err
		}
		if input.Attempt == math.MaxInt64 {
			return h.failure(errors.New("attempt ordinal exhausted")), errors.New("attempt ordinal exhausted")
		}
		wait := executorBackoff(input.Execution.RetryBackoff.Value, retry+1)
		retryAt := time.Now().Add(wait)
		result = recordNodeFailure(input.NodeID, result, h.deps.Masker.Mask)
		sourceAttempt := input.Attempt
		applied, err := h.transition(ctx, *input, store.RetryNode{SourceAttempt: &sourceAttempt, Result: &result, RetryAt: &retryAt})
		if err != nil {
			return result, err
		}
		if applied.Generation != input.Generation || applied.Attempt != input.Attempt+1 {
			return result, errors.New("retry transition returned an inconsistent claim or attempt")
		}
		input.Attempt = applied.Attempt
		bound, bindErr := h.bindStorage(ctx, *input)
		if bindErr != nil {
			input.Storage = StorageBinding{}
			return h.failure(bindErr), bindErr
		}
		*input = bound
		after := ""
		if wait > 0 {
			after = " after " + wait.String()
		}
		if err := h.diagnostic(ctx, *input, "info", "retry", "", fmt.Sprintf("retry attempt %d/%d%s", row.ApplicationRetries+retry+2, input.Execution.Retry.Value+1, after), map[string]any{
			"attempt": row.ApplicationRetries + retry + 2, "total": input.Execution.Retry.Value + 1, "ordinal": input.Attempt,
		}); err != nil {
			return h.failure(err), err
		}
		if err = executorWait(ctx, wait); err != nil {
			return h.failure(err), err
		}
	}
}

func (h *nodeExecutor) transition(ctx context.Context, input NodeInput, command store.NodeCommand) (store.Applied, error) {
	if err := context.Cause(ctx); err != nil {
		return store.Applied{}, err
	}
	command = store.BindNodeCommand(command, controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID)}, executorActor(input))
	applied, err := h.deps.Store.TransitionNode(ctx, command)
	if err != nil {
		return applied, &executorTransitionError{err: err}
	}
	return applied, nil
}

func executorActor(input NodeInput) store.Actor {
	return store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: input.Authority.CapabilityPrefix}, CapabilityPrefix: input.Authority.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(input.Accepted.RunID), NodeID: runspec.NodeID(input.NodeID), Holder: input.Authority.Holder, Generation: input.Generation}}
}

func (h *nodeExecutor) report(ctx context.Context, input NodeInput, result store.NodeResult, runErr error) error {
	var refused *executorTransitionError
	if errors.As(runErr, &refused) {
		return runErr
	}
	if err := executorLostAuthority(ctx); err != nil {
		return errors.Join(runErr, err)
	}
	if ctx.Err() != nil {
		result = store.NodeResult{Outcome: store.NodeCancelled, Reason: string(apierr.ReasonCancelled), MaskedError: limits.Error(h.deps.Masker.Mask(context.Cause(ctx).Error()))}
		var admission *admissionFailure
		if errors.As(context.Cause(ctx), &admission) && admission.Outcome == store.NodeSuperseded {
			result.Outcome, result.Reason = admission.Outcome, admission.Reason
		}
	}
	if result.Outcome == "" {
		result = h.failure(runErr)
	}
	// safety: a cancelled context cannot send the final fenced report; the store still verifies current authority.
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.HeartbeatTimeout)
	defer cancel()
	result = recordNodeFailure(input.NodeID, result, h.deps.Masker.Mask)
	_, err := h.transition(reportCtx, input, store.ReportNode{SourceAttempt: &input.Attempt, Outcome: result.Outcome, Result: &result})
	return errors.Join(runErr, err)
}

type executorTransitionError struct{ err error }

func (e *executorTransitionError) Error() string { return e.err.Error() }
func (e *executorTransitionError) Unwrap() error { return e.err }

func (h *nodeExecutor) failure(err error) store.NodeResult {
	return h.failureAt(err, "action")
}

func (h *nodeExecutor) failureAt(err error, stage string) store.NodeResult {
	result := store.NodeResult{Outcome: store.NodeFailed}
	if err != nil {
		result.MaskedError = limits.Error(h.deps.Masker.Mask(err.Error()))
	}
	var failure *apierr.Error
	code := apierr.SDKError
	if errors.As(err, &failure) {
		code = maskedFailureCode(failure.Code, h.deps.Masker.Mask)
		result.Reason = string(code)
	}
	result.Failure = &nodeproto.JobFailure{Stage: stage, Error: &apierr.Error{Code: code, Message: result.MaskedError}}
	return result
}

func executorLostAuthority(ctx context.Context) error {
	err := context.Cause(ctx)
	if errors.Is(err, lease.ErrLeaseLost) || errors.Is(err, lease.ErrSilent) {
		return err
	}
	return nil
}

func executorError(code apierr.Code, message string) error {
	return &apierr.Error{Code: code, Message: message}
}

func executorBackoff(initial time.Duration, ordinal int) time.Duration {
	wait := min(initial, limits.RetryBackoff)
	for i := 1; i < ordinal && wait > 0; i++ {
		if wait > limits.RetryBackoff/2 {
			return limits.RetryBackoff
		}
		wait *= 2
	}
	return max(0, wait)
}

func executorWait(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return context.Cause(ctx)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

var _ NodeTransitionStore = (*store.Tenant)(nil)

func (h *nodeExecutor) bindStorage(ctx context.Context, input NodeInput) (NodeInput, error) {
	if h.deps.BindStorage == nil {
		if input.Storage.Bytes != nil || input.Storage.Records != nil || input.Storage.RemoteCache != nil || input.Storage.Authority.CapabilityRef != "" {
			return input, errors.New("job storage requires a fresh binding factory")
		}
		return input, nil
	}
	binding, err := h.deps.BindStorage(ctx, input)
	if err != nil {
		return input, err
	}
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		return input, err
	}
	authority := binding.Authority
	attempt := runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Attempt: int64(input.Attempt), Generation: int64(input.Generation)}
	if binding.Bytes == nil || binding.Records == nil || authority.CapabilityRef == "" || authority.Principal == "" || !authority.Attempt.Set || authority.Attempt.Value != attempt || authority.Repo == "" || spec.Request.Request.Git.Repository.Key != "" && authority.Repo != spec.Request.Request.Git.Repository.Key {
		return input, errors.New("storage binding differs from its committed job attempt")
	}
	if spec.Request.Request.Origin != runspec.OriginLocal || spec.Request.Request.Options.CacheFrom.Value == "" {
		binding.RemoteCache = nil
	} else if binding.RemoteCache == nil {
		binding.RemoteCache = input.Storage.RemoteCache
	}
	input.Storage = binding
	return input, nil
}
