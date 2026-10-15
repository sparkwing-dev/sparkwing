package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// EvaluationStore exposes accepted declarations without granting state writes.
type EvaluationStore interface {
	ReadNode(context.Context, store.NodeRef) (store.NodeRow, error)
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
}

// EvaluationInput binds owner-side code to accepted declarations. RouteBinding
// must be delegated from the live owner or eval unit, never an ended source claim.
// Replay contains recorded ancestor outputs when this job was itself generated.
type EvaluationInput struct {
	processRevoke func() error
	Engine        engine.Resolved
	Accepted      AcceptedRun
	NodeID        string
	Generation    int64
	SpecHash      string
	Entry         *source.Entry
	GrantedRunner nodeproto.RunnerIdentity
	RouteBinding  nodeproto.ProcessBinding
	Replay        []GeneratorReplay
	Storage       StorageBinding
}

// EvaluationDependencies connects the evaluator to the same engine, protocol and
// validation boundaries as execution. ReadOutput must authorize the persisted
// terminal generation and return its recorded JSON, including explicit null.
// Bind delegates a fresh process bearer; the host revokes it before joining and cleanup.
// Start owns run-log attribution, masking and environment. Bound input requires Bind.
// ChildContext applies the shared deadline policy to skip and memo evaluation.
type EvaluationDependencies struct {
	Store         EvaluationStore
	Bind          func(context.Context, EvaluationInput) (ProcessGrant, error)
	Start         func(context.Context, EvaluationInput) (ExecutorProcess, error)
	EnsureEngine  func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error)
	ValidatePlan  func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	CanonicalHash func(any) (runspec.Hash, error)
	ChildContext  func(context.Context, time.Duration, time.Duration) (context.Context, context.CancelFunc, error)
	Validation    nodeproto.ValidationContext
	ReadOutput    func(context.Context, store.NodeRef, int64) (json.RawMessage, error)
	Mask          func(string) string
}

// OwnerEvaluator reconstructs accepted code and evaluates owner closures. It
// never executes a job body or writes topology; OwnerHost submits its result.
type OwnerEvaluator struct {
	deps EvaluationDependencies
	sdkCode
}

// NewOwnerEvaluator requires explicit authority, output and process adapters.
func NewOwnerEvaluator(deps EvaluationDependencies) (*OwnerEvaluator, error) {
	if deps.Store == nil || deps.Start == nil || deps.EnsureEngine == nil || deps.Mask == nil {
		return nil, errors.New("owner evaluator requires store, process, engine and masking adapters")
	}
	if deps.ValidatePlan == nil {
		deps.ValidatePlan = nodeproto.ValidatePlan
	}
	if deps.CanonicalHash == nil {
		deps.CanonicalHash = runspec.CanonicalHash
	}
	if deps.ChildContext == nil {
		deps.ChildContext = limits.ChildContext
	}
	return &OwnerEvaluator{deps: deps, sdkCode: sdkCode{Store: deps.Store, ReadOutput: deps.ReadOutput, ValidatePlan: deps.ValidatePlan, CanonicalHash: deps.CanonicalHash, Validation: deps.Validation}}, nil
}

// Expand evaluates every declared generator from the source's recorded output
// in one reconstructed SDK process. Failed evaluation returns only a masked
// failure; cancellation and stale input leave the barrier to the live owner.
func (h *OwnerEvaluator) Expand(ctx context.Context, input EvaluationInput) (json.RawMessage, *store.ExpansionFailure, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, nil, err
	}
	spec, row, job, err := h.accepted(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	if row.Generation < 0 || row.Status != store.StatusDone || (row.Outcome != store.NodeSuccess && row.Outcome != store.NodeMemoized) || row.Expansion != store.ExpansionExpected || len(job.Closures.Generate) == 0 {
		return nil, nil, executorError(apierr.IllegalTransition, "expansion requires a successful source with expected generators")
	}
	output, err := h.readResult(ctx, row, input.Storage)
	if err == nil && !json.Valid(output) {
		err = executorError(apierr.ProtocolError, "generator source output is missing or invalid JSON")
	}
	var body json.RawMessage
	if err == nil {
		body, err = h.expand(ctx, input, spec, job, output)
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, nil, errors.Join(cause, err)
	}
	if err != nil {
		code := apierr.SDKError
		var structured *apierr.Error
		if errors.As(err, &structured) {
			code = structured.Code
		}
		return nil, &store.ExpansionFailure{Code: code, MaskedError: boundedNodeErrText(h.deps.Mask(err.Error()))}, nil
	}
	return body, nil, nil
}

func (h *OwnerEvaluator) accepted(ctx context.Context, input EvaluationInput) (runspec.Spec, store.NodeRow, nodeproto.JobSpec, error) {
	var spec runspec.Spec
	var row store.NodeRow
	var job nodeproto.JobSpec
	if input.Accepted.RunID == "" || input.NodeID == "" || input.SpecHash == "" || input.Entry == nil {
		return spec, row, job, errors.New("evaluation requires accepted source and job identity")
	}
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil || input.Accepted.SpecHash == "" || string(spec.Hash) != input.Accepted.SpecHash || string(spec.PlanHash) != input.Accepted.PlanHash {
		return spec, row, job, executorError(apierr.SpecDrift, "evaluation does not match the accepted run seal")
	}
	if !reflect.DeepEqual(spec.Resolved.Engine, input.Engine.Identity) || !reflect.DeepEqual(input.Entry.Engine.Identity, input.Engine.Identity) {
		return spec, row, job, executorError(apierr.SpecDrift, "evaluation must use the sealed engine")
	}
	if input.RouteBinding.RunID != input.Accepted.RunID || input.RouteBinding.JobID != input.NodeID {
		return spec, row, job, executorError(apierr.SpecDrift, "evaluation binding does not match the accepted job")
	}
	hash, err := h.CanonicalHash(input.Accepted.Plan)
	if err != nil {
		return spec, row, job, err
	}
	if string(hash) != input.Accepted.PlanHash {
		return spec, row, job, executorError(apierr.PlanDrift, "evaluation plan does not match its accepted hash")
	}
	row, err = h.Store.ReadNode(ctx, store.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID})
	if err != nil {
		return spec, row, job, err
	}
	if row.Ref.RunID != input.Accepted.RunID || row.Ref.NodeID != input.NodeID || row.Generation != input.Generation || row.SpecHash != input.SpecHash {
		return spec, row, job, executorError(apierr.SpecDrift, "evaluation source differs from persisted identity")
	}
	if err = json.Unmarshal(row.Spec, &job); err != nil || job.ID != input.NodeID {
		return spec, row, job, executorError(apierr.PlanDrift, "evaluation source declaration is invalid")
	}
	hash, err = h.CanonicalHash(job)
	if err != nil {
		return spec, row, job, err
	}
	if string(hash) != row.SpecHash {
		return spec, row, job, executorError(apierr.PlanDrift, "evaluation source differs from its accepted hash")
	}
	return spec, row, job, nil
}

func (h *OwnerEvaluator) expand(ctx context.Context, input EvaluationInput, spec runspec.Spec, job nodeproto.JobSpec, output json.RawMessage) (body json.RawMessage, err error) {
	err = h.withProcess(ctx, input, spec, func(session *executorSession, code NodeInput) error {
		body, err = h.expansionBody(ctx, session, code, job, output)
		return err
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (h *OwnerEvaluator) expansionBody(ctx context.Context, session *executorSession, code NodeInput, job nodeproto.JobSpec, output json.RawMessage) (json.RawMessage, error) {
	snapshot, err := h.Store.ReadSnapshot(ctx, code.Accepted.RunID)
	if err != nil {
		return nil, err
	}
	if snapshot.Run.ID != code.Accepted.RunID {
		return nil, executorError(apierr.SpecDrift, "expansion snapshot belongs to another run")
	}
	validation := h.Validation
	validation.Expansion, validation.SourceJob = true, code.NodeID
	validation.ExistingConcurrency = code.Accepted.Plan.Concurrency
	validation.ExistingJobs = make([]nodeproto.JobSpec, 0, len(snapshot.Nodes))
	for _, row := range snapshot.Nodes {
		var existing nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &existing); err != nil || row.Ref.RunID != code.Accepted.RunID || row.Ref.NodeID != existing.ID {
			return nil, executorError(apierr.PlanDrift, "expansion snapshot has invalid declarations")
		}
		validation.ExistingJobs = append(validation.ExistingJobs, existing)
	}
	body, err := h.generate(ctx, session, code, job, output)
	if err != nil {
		return nil, err
	}
	_, err = h.ValidatePlan(body, validation)
	return body, err
}

var _ EvaluationStore = (*store.Tenant)(nil)
