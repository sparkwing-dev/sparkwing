package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func configureNativeAdmission(owner *OwnerHost, local *LocalAdmission, capacity *StoreAdmission) {
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
		if err := requireCapacity(capacity, local, row.Execution.Concurrency); err != nil {
			return err
		}
		work := body
		isLocal := owner.Local != nil && owner.Local(row)
		if local != nil && !isLocal {
			for _, group := range row.Execution.Concurrency {
				if group.Scope == "run" || group.Scope == "box" {
					return errors.New("remote work requires a retained local semaphore adapter")
				}
			}
		}
		if local != nil && isLocal {
			work = func(ctx context.Context) error { return local.Node(ctx, accepted, row, body) }
		}
		if capacity != nil {
			return capacity.Node(ctx, accepted, row, work)
		}
		return work(ctx)
	}
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
