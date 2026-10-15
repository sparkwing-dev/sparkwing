package nodehost

import (
	"context"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func configureNativeAdmission(owner *OwnerHost, local *LocalAdmission, capacity *StoreAdmission) {
	owner.retainRemote = local != nil
	owner.bindAdmission = func(bound *OwnerHost, actor store.Actor) {
		if capacity == nil {
			return
		}
		copy := *capacity
		copy.Owner = actor
		configureNativeAdmission(bound, local, &copy)
	}
	if local != nil {
		copy := *local
		copy.storeGroups = capacity != nil
		local = &copy
	}
	if capacity != nil {
		copy := *capacity
		copy.local = local != nil
		capacity = &copy
	}
	owner.Around = func(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (RunCompletion, error) {
		spec, err := admissionSpec(accepted)
		if err != nil {
			return RunCompletion{}, err
		}
		if err := requireCapacity(capacity, local, spec.Resolved.Settings.Concurrency); err != nil {
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
		if err := requireMachineAdmission(local, isLocal); err != nil {
			return err
		}
		if err := requireCapacity(capacity, local, row.Execution.Concurrency); err != nil {
			return err
		}
		work := body
		if local != nil {
			work = func(ctx context.Context) error { return local.node(ctx, accepted, row, isLocal, body) }
		}
		if capacity != nil {
			return capacity.Node(ctx, accepted, row, work)
		}
		return work(ctx)
	}
}

func requireMachineAdmission(local *LocalAdmission, isLocal bool) error {
	if isLocal && local == nil {
		return errors.New("local execution requires machine admission")
	}
	return nil
}

func hasLocalConcurrency(groups []runspec.Concurrency) bool {
	return slices.ContainsFunc(groups, func(group runspec.Concurrency) bool { return group.Scope == "run" || group.Scope == "box" })
}

func requireCapacity(capacity *StoreAdmission, local *LocalAdmission, groups []runspec.Concurrency) error {
	if capacity != nil {
		return nil
	}
	for _, group := range groups {
		if local == nil || group.Scope == "global" {
			return errors.New("concurrency admission requires a capacity store")
		}
	}
	return nil
}
