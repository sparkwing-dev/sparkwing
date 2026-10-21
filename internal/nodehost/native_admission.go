package nodehost

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NativeAdmission keeps logical concurrency independent of optional machine compute.
type NativeAdmission struct {
	capacity *storeAdmission
	compute  *LocalAdmission
	required bool
}

// ComposeNativeAdmission binds store-owned concurrency independently of compute.
// Required compute is used by requesters; runners may omit it without losing concurrency.
func ComposeNativeAdmission(capacity CapacityStore, compute *LocalAdmission, required bool) NativeAdmission {
	admission := NativeAdmission{required: required}
	if compute != nil {
		copy := *compute
		admission.compute = &copy
	}
	if capacity != nil {
		admission.capacity = &storeAdmission{Store: capacity}
	}
	return admission
}

func configureNativeAdmission(owner *OwnerHost, local *LocalAdmission, capacity *storeAdmission, required bool) {
	owner.retainRemote = local != nil
	owner.executionAdmission = NativeAdmission{capacity: capacity}
	owner.bindAdmission = func(bound *OwnerHost, actor store.Actor) {
		if capacity == nil {
			return
		}
		copy := *capacity
		copy.Owner = actor
		configureNativeAdmission(bound, local, &copy, required)
	}
	check := func(groups []runspec.Concurrency) error {
		if required && local == nil {
			return errors.New("local execution requires machine admission")
		}
		return requireCapacity(capacity, groups)
	}
	if owner.AdmissionPolicy == nil {
		owner.AdmissionPolicy = func(_ context.Context, _ EvaluationInput, row store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
			return OwnerLayerResult{}, check(row.Execution.Concurrency)
		}
	}
	owner.claimAdmission = local
	owner.Around = func(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (RunCompletion, error) {
		spec, err := admissionSpec(accepted)
		if err != nil {
			return RunCompletion{}, err
		}
		if err := check(spec.Resolved.Settings.Concurrency); err != nil {
			return RunCompletion{}, err
		}
		work := body
		if local != nil {
			work = func(ctx context.Context) (RunCompletion, error) { return local.Run(ctx, accepted, body) }
		}
		if capacity != nil {
			return capacity.Run(ctx, accepted, work)
		}
		return work(ctx)
	}
	owner.Admit = func(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
		isLocal := owner.Local != nil && owner.Local(row)
		if err := check(row.Execution.Concurrency); err != nil {
			return err
		}
		work := body
		if local != nil {
			spec, err := admissionSpec(accepted)
			if err != nil {
				return err
			}
			work = func(ctx context.Context) error {
				return local.node(ctx, accepted, row, isLocal && spec.Request.Request.Burst == nil, body)
			}
		}
		return work(ctx)
	}
}

func hasLocalConcurrency(groups []runspec.Concurrency) bool {
	return slices.ContainsFunc(groups, func(group runspec.Concurrency) bool { return group.Scope == "run" || group.Scope == "box" })
}

func requireCapacity(capacity *storeAdmission, groups []runspec.Concurrency) error {
	if capacity == nil && len(groups) != 0 {
		return errors.New("concurrency admission requires a capacity store")
	}
	return nil
}

func (a NativeAdmission) claimedNode(ctx context.Context, input NodeInput, body func(context.Context) error) error {
	if a.required && a.compute == nil {
		return errors.New("local execution requires machine admission")
	}
	if err := requireCapacity(a.capacity, input.Execution.Concurrency); err != nil {
		return err
	}
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.Accepted.RunID, NodeID: input.NodeID}, Generation: input.Generation, Execution: input.Execution}
	work := body
	if a.compute != nil {
		work = func(ctx context.Context) error { return a.compute.ClaimedNode(ctx, input.Accepted, row, body) }
	}
	if a.capacity != nil {
		capacity := *a.capacity
		capacity.Owner = executorActor(input)
		return capacity.Node(ctx, input.Accepted, row, work)
	}
	return work(ctx)
}
