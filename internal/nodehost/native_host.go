package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"

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

// NativeHostConfig supplies policy adapters in nested configs; shared fields come from its top level.
type NativeHostConfig struct {
	Store          NativeHostStore
	Lease          lease.Keeper
	Source         source.Options
	Snapshots      CapturedEnvironmentLoader
	Masks          *MaskRegistry
	Processes      *NativeProcesses
	Router         ProcessEndpointRegistrar
	Validation     nodeproto.ValidationContext
	ValidatePlan   func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	CanonicalHash  func(any) (runspec.Hash, error)
	Owner          OwnerHost
	Executor       ExecutorDependencies
	Evaluation     EvaluationDependencies
	Planner        SDKPlanner
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
	if config.Owner.Wait == nil || config.Owner.Renew == nil || config.Planner.Log == nil {
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
	validation.Secrets = config.Masks.replayMasks
	functions := (PlanningFunctions{Validate: config.ValidatePlan, Hash: config.CanonicalHash}).defaults()
	execution := config.Executor
	if execution.ReadOutput == nil {
		execution.ReadOutput = config.Evaluation.ReadOutput
	}
	if execution.Log == nil {
		execution.Log = func(ctx context.Context, input NodeInput, frame nodeproto.FrameLog) error {
			return processes.Log(ctx, input.Accepted.RunID, input.NodeID, frame)
		}
	}
	execution.Masks = config.Masks
	execution.Store, execution.Masker = config.Store, config.Masks.masker
	execution.Start, execution.EnsureEngine = processes.Start, config.Source.Engines.Ensure
	execution.Validation, execution.ValidatePlan, execution.CanonicalHash = validation, functions.Validate, functions.Hash
	executor, err := NewNodeExecutor(execution)
	if err != nil {
		return nil, nil, nil, err
	}
	evaluation := config.Evaluation
	evaluation.Masks = config.Masks
	evaluation.Store, evaluation.Masker = config.Store, config.Masks.masker
	evaluation.Start, evaluation.EnsureEngine = processes.StartEvaluation, config.Source.Engines.Ensure
	evaluation.Validation, evaluation.ValidatePlan, evaluation.CanonicalHash = validation, functions.Validate, functions.Hash
	evaluator, err := NewOwnerEvaluator(evaluation)
	if err != nil {
		return nil, nil, nil, err
	}
	planner := config.Planner
	planner.Masks = config.Masks
	planner.Environment = processes.Session.Environments.planning
	if planner.CacheDirectory == "" && config.Source.Workspace != "" {
		planner.CacheDirectory = filepath.Join(config.Source.Workspace, "plan-cache")
	}
	planner.Start, planner.EnsureEngine = processes.StartPlanning, config.Source.Engines.Ensure
	planner.Masker, planner.ValidatePlan, planner.CanonicalHash = config.Masks.masker, functions.Validate, functions.Hash
	owner := config.Owner
	owner.Store, owner.Lease, owner.Source = config.Store, config.Lease, config.Source
	owner.Computing = processes.Computing
	owner.Snapshots, owner.Masks, owner.Masker = config.Snapshots, config.Masks, config.Masks.masker
	owner.Executor, owner.Planner = executor, &planner
	if config.Capacity == nil {
		if capacity, ok := config.Store.(CapacityStore); ok {
			config.Capacity = &StoreAdmission{Store: capacity}
		}
	}
	configureNativeAdmission(&owner, config.Admission, config.Capacity)
	owner.Planning.Validation = validation
	layers := owner.Layer
	owner.Layer = func(ctx context.Context, layer LayerSpec, input EvaluationInput, row store.NodeRow, storage StorageBinding) (OwnerLayerResult, error) {
		if layer.Name == "debug" {
			unused, err := unusedOwnerPolicy(layer.Name, input, row, owner.Local)
			if err != nil || unused || owner.Debug != nil {
				return OwnerLayerResult{}, err
			}
			return OwnerLayerResult{}, errors.New("debug pause requires an authority-bearing boundary adapter")
		}
		if layer.Name == "skip_if" {
			return evaluator.skipLayer(ctx, input, row, processes.Log)
		}
		if layer.Name == "approval" {
			return OwnerLayerResult{}, nil
		}
		if layer.Name == "admission" {
			isLocal := owner.Local != nil && owner.Local(row)
			if err := requireMachineAdmission(config.Admission, isLocal); err != nil {
				return OwnerLayerResult{}, err
			}
			if config.Capacity != nil || isLocal {
				return OwnerLayerResult{}, nil
			}
		}
		if layers == nil {
			unused, err := unusedOwnerPolicy(layer.Name, input, row, owner.Local)
			if err != nil || unused {
				return OwnerLayerResult{}, err
			}
			return OwnerLayerResult{}, errors.New("native host requires the " + layer.Name + " owner policy adapter")
		}
		return layers(ctx, layer, input, row, storage)
	}
	if owner.Expand == nil {
		owner.Expand = func(ctx context.Context, input EvaluationInput, _ store.NodeRow, _ StorageBinding) (json.RawMessage, runspec.SecretRegistration, *store.ExpansionFailure, error) {
			return evaluator.Expand(ctx, input)
		}
	}
	claimed := &ClaimedNodeHost{
		Store: config.Store, Lease: config.Lease, Source: config.Source, Snapshots: config.Snapshots,
		Executor: executor, Masker: config.Masks.masker, Masks: config.Masks, Admission: config.ClaimAdmission,
	}
	if err := RegisterProcessServices(config.Router, processes.Authorities.Resolve); err != nil {
		return nil, nil, nil, err
	}
	return &owner, claimed, evaluator, nil
}

func unusedOwnerPolicy(name string, input EvaluationInput, row store.NodeRow, local func(store.NodeRow) bool) (bool, error) {
	var spec runspec.Spec
	var job nodeproto.JobSpec
	if json.Unmarshal(input.Accepted.Spec, &spec) != nil || json.Unmarshal(row.Spec, &job) != nil {
		return false, errors.New("owner policy requires accepted run and job declarations")
	}
	switch name {
	case "debug":
		pause := spec.Resolved.Settings.Options.Pause.Value
		return job.OnFailureOf != "" || !slices.Contains(pause.Before.Value, job.ID) && (job.Approval != nil || !slices.Contains(pause.After.Value, job.ID) && !pause.OnFailure.Value), nil
	case "memo":
		return job.Memo == nil && job.Closures.MemoKey == "", nil
	case "admission":
		return len(spec.Resolved.Settings.Concurrency) == 0 && len(row.Execution.Concurrency) == 0 && (local == nil || !local(row)), nil
	default:
		return false, nil
	}
}
