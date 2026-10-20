package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NativeHostStore supplies the persisted state shared by owner and claimed execution.
type NativeHostStore interface {
	OwnerStore
	ClaimStore
}

// NativeHostConfig supplies external dependencies once for the shared host.
type NativeHostConfig struct {
	Store          NativeHostStore
	Lease          lease.Keeper
	Source         source.Options
	Snapshots      CapturedEnvironmentLoader
	Masks          *MaskRegistry
	Processes      *NativeProcesses
	Router         ProcessEndpointRegistrar
	Validation     nodeproto.ValidationContext
	Owner          OwnerOperations
	Executor       ExecutionOperations
	Evaluation     EvaluationOperations
	ReadOutput     func(context.Context, controllerwire.NodeRef, int64) (json.RawMessage, error)
	PlanningLog    func(context.Context, nodeproto.FrameLog) error
	Admission      *LocalAdmission
	ClaimAdmission *LocalAdmission
	Capacity       *StoreAdmission
}

// NewNativeHost configures its owned transport and returns shared hosts and an evaluator for owner policies.
func NewNativeHost(config NativeHostConfig) (*OwnerHost, *ClaimedNodeHost, *OwnerEvaluator, error) {
	if config.Store == nil || config.Source.Engines == nil || config.Masks == nil || config.Masks.masker == nil || config.Processes == nil || config.Processes.Authorities == nil || config.Processes.Log == nil || config.Router == nil {
		return nil, nil, nil, errors.New("native host requires store, source engine, shared masks and process transport")
	}
	if config.Lease.Interval <= 0 || config.Lease.Lease <= config.Lease.Interval {
		return nil, nil, nil, errors.New("native host requires a positive renewal interval below its lease")
	}
	if config.Owner.Wait == nil || config.Owner.Renew == nil || config.PlanningLog == nil {
		return nil, nil, nil, errors.New("native host requires unit renewal, state following and planning logs")
	}
	if config.Executor.Bind == nil || config.Evaluation.Bind == nil {
		return nil, nil, nil, errors.New("native host requires fresh execution and evaluation delegation")
	}
	config.Source.Base.Masker = config.Masks.masker
	config.Source.BuildEnvironment = config.Masks.environment(config.Source.BuildEnvironment)
	processes := config.Processes
	processes.Masks = config.Masks
	processes.Session.Environments.Masks = config.Masks
	processes.Session.Environments.Store = config.Store
	processes.Session.Environments.Base = config.Source.Base
	processes.Session.Environments.Snapshots = config.Snapshots
	validation := config.Validation
	validation.Secrets = config.Masks
	functions := (PlanningFunctions{}).defaults()
	execution := ExecutorDependencies{Log: config.Executor.Log, BindStorage: config.Executor.BindStorage, Bind: config.Executor.Bind, ChildContext: config.Executor.ChildContext, PrepareArtifacts: config.Executor.PrepareArtifacts, PrepareCaches: config.Executor.PrepareCaches, PublishOutput: config.Executor.PublishOutput, Annotate: config.Executor.Annotate, ReadFailure: config.Executor.ReadFailure, ReadOutput: config.ReadOutput}
	if execution.Log == nil {
		execution.Log = func(ctx context.Context, input NodeInput, frame nodeproto.FrameLog) error {
			return processes.Log(ctx, input.Accepted.RunID, input.NodeID, frame)
		}
	}
	execution.Masks = config.Masks
	execution.Store, execution.Mask = config.Store, config.Masks.masker.Mask
	execution.Start, execution.EnsureEngine = processes.Start, config.Source.Engines.Ensure
	execution.Validation, execution.ValidatePlan, execution.CanonicalHash = validation, functions.Validate, functions.Hash
	executor, err := NewNodeExecutor(execution)
	if err != nil {
		return nil, nil, nil, err
	}
	evaluation := EvaluationDependencies{Bind: config.Evaluation.Bind, ChildContext: config.Evaluation.ChildContext, ReadOutput: config.ReadOutput}
	evaluation.Masks = config.Masks
	evaluation.Store, evaluation.Mask = config.Store, config.Masks.masker.Mask
	evaluation.Start, evaluation.EnsureEngine = processes.StartEvaluation, config.Source.Engines.Ensure
	evaluation.Validation, evaluation.ValidatePlan, evaluation.CanonicalHash = validation, functions.Validate, functions.Hash
	evaluator, err := NewOwnerEvaluator(evaluation)
	if err != nil {
		return nil, nil, nil, err
	}
	planner := SDKPlanner{Log: config.PlanningLog, Masks: config.Masks, Start: processes.StartPlanning, EnsureEngine: config.Source.Engines.Ensure, Masker: config.Masks.masker, ValidatePlan: functions.Validate, CanonicalHash: functions.Hash}
	owner := OwnerHost{Renew: config.Owner.Renew, Acquire: config.Owner.Acquire, Prepare: config.Owner.Prepare, Project: config.Owner.Project, Decide: config.Owner.Decide, Wait: config.Owner.Wait, WaitNode: config.Owner.WaitNode, Debug: config.Owner.Debug, MemoPolicy: config.Owner.MemoPolicy, AdmissionPolicy: config.Owner.AdmissionPolicy, Local: config.Owner.Local, AdoptClaim: config.Owner.AdoptClaim, Delegator: config.Owner.Delegator, ExpansionActor: config.Owner.ExpansionActor, Expand: config.Owner.Expand}
	owner.Store, owner.Lease, owner.Source = config.Store, config.Lease, config.Source
	owner.Computing = processes.Computing
	owner.Snapshots, owner.Masks, owner.Mask = config.Snapshots, config.Masks, config.Masks.masker.Mask
	owner.Executor, owner.Planner = executor, &planner
	if config.Capacity == nil {
		if capacity, ok := config.Store.(CapacityStore); ok {
			config.Capacity = &StoreAdmission{Store: capacity}
		}
	}
	configureNativeAdmission(&owner, config.Admission, config.Capacity)
	owner.Planning.Validation = validation
	owner.DebugPolicy = func(ctx context.Context, input EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		var spec runspec.Spec
		var job nodeproto.JobSpec
		if json.Unmarshal(input.Accepted.Spec, &spec) != nil || json.Unmarshal(row.Spec, &job) != nil {
			return OwnerLayerResult{}, errors.New("debug policy requires accepted run and job declarations")
		}
		pause := spec.Resolved.Settings.Options.Pause.Value
		unused := job.OnFailureOf != "" || !slices.Contains(pause.Before.Value, job.ID) && (job.Approval != nil || !slices.Contains(pause.After.Value, job.ID) && !pause.OnFailure.Value)
		if unused || owner.Debug != nil {
			return OwnerLayerResult{}, nil
		}
		return OwnerLayerResult{}, errors.New("debug pause requires an authority-bearing boundary adapter")
	}
	owner.SkipPolicy = func(ctx context.Context, input EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
		return evaluator.skipLayer(ctx, input, row, processes.Log)
	}
	owner.ApprovalPolicy = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
		return OwnerLayerResult{}, nil
	}
	if owner.MemoPolicy == nil {
		owner.MemoPolicy = func(_ context.Context, _ EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
			var job nodeproto.JobSpec
			if err := json.Unmarshal(row.Spec, &job); err != nil {
				return OwnerLayerResult{}, err
			}
			if job.Memo != nil || job.Closures.MemoKey != "" {
				return OwnerLayerResult{}, errors.New("native host requires the memo owner policy adapter")
			}
			return OwnerLayerResult{}, nil
		}
	}
	if owner.AdmissionPolicy == nil {
		owner.AdmissionPolicy = func(_ context.Context, input EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
			local := owner.Local != nil && owner.Local(row)
			if err := requireMachineAdmission(config.Admission, local); err != nil {
				return OwnerLayerResult{}, err
			}
			if config.Capacity != nil || local {
				return OwnerLayerResult{}, nil
			}
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				return OwnerLayerResult{}, err
			}
			if len(spec.Resolved.Settings.Concurrency) != 0 || len(row.Execution.Concurrency) != 0 {
				return OwnerLayerResult{}, errors.New("native host requires the admission owner policy adapter")
			}
			return OwnerLayerResult{}, nil
		}
	}
	if owner.Expand == nil {
		owner.Expand = func(ctx context.Context, input EvaluationInput, _ store.NodeRow, _ StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
			return evaluator.Expand(ctx, input)
		}
	}
	claimed := &ClaimedNodeHost{
		Store: config.Store, Lease: config.Lease, Source: config.Source, Snapshots: config.Snapshots,
		Executor: executor, Mask: config.Masks.masker.Mask, Masks: config.Masks, Admission: config.ClaimAdmission,
	}
	if err := RegisterProcessServices(config.Router, processes.Authorities.Resolve); err != nil {
		return nil, nil, nil, err
	}
	return &owner, claimed, evaluator, nil
}

// OwnerOperations supplies placement callbacks without shared host state.
type OwnerOperations struct {
	Renew           func(context.Context, store.Actor, bool) (store.LeaseBeat, error)
	Acquire         func(context.Context, RunInput) (*source.Entry, error)
	Prepare         func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error)
	Project         func(store.RunSnapshot) (store.ScheduleSnapshot, error)
	Decide          func(store.ScheduleSnapshot) store.ScheduleDecision
	Wait            func(context.Context, store.RunSnapshot) error
	WaitNode        WaitNodeChange
	Debug           DebugBoundaryWaiter
	MemoPolicy      OwnerPolicy
	AdmissionPolicy OwnerPolicy
	Local           func(store.NodeRow) bool
	AdoptClaim      func(context.Context, store.ReconciledNode) error
	Delegator       OwnerDelegator
	ExpansionActor  func(context.Context, RunInput, store.NodeRow) (store.Actor, error)
	Expand          func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error)
}

// ExecutionOperations supplies placement callbacks without shared host state.
type ExecutionOperations struct {
	Log              func(context.Context, NodeInput, nodeproto.FrameLog) error
	BindStorage      func(context.Context, NodeInput) (StorageBinding, error)
	Bind             func(context.Context, NodeInput) (ProcessGrant, error)
	ChildContext     func(context.Context, time.Duration, time.Duration) (context.Context, context.CancelFunc, error)
	PrepareArtifacts func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error)
	PrepareCaches    func(context.Context, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error)
	PublishOutput    func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error)
	Annotate         func(context.Context, NodeInput, nodeproto.AnnotationRequest) error
	ReadFailure      func(context.Context, controllerwire.NodeRef, int64) (*nodeproto.JobFailure, error)
}

// EvaluationOperations supplies placement callbacks without shared host state.
type EvaluationOperations struct {
	Bind         func(context.Context, EvaluationInput) (ProcessGrant, error)
	ChildContext func(context.Context, time.Duration, time.Duration) (context.Context, context.CancelFunc, error)
}
