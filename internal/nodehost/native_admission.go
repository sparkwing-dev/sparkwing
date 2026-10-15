package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func configureNativeAdmission(owner *OwnerHost, local *LocalAdmission, capacity *StoreAdmission) {
	if local == nil && capacity == nil {
		return
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
