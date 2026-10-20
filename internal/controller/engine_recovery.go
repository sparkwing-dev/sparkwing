package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	controllerRecoveryBatch = 500
	controllerOwnerLeaseTTL = time.Minute
)

func (s *Server) reconcileOwnedRuns(ctx context.Context) {
	if _, err := reconcileControllerRuns(ctx, s.operator, s.measureHolder()); err != nil {
		s.logger.Error("controller run recovery failed", "err", err)
	}
}

func reconcileControllerRuns(ctx context.Context, operator store.OperatorStore, holder string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	lease, err := operator.AcquireSingletonLease(ctx, "controller.owner", holder, controllerOwnerLeaseTTL)
	if errors.Is(err, store.ErrLockHeld) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count, after := 0, ""
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		page, err := operator.ReconcileControllerRuns(ctx, lease, after, controllerRecoveryBatch)
		count += len(page)
		if err != nil || len(page) < controllerRecoveryBatch {
			return count, err
		}
		after = page[len(page)-1].RunID
		lease, err = operator.RenewSingletonLease(ctx, lease, controllerOwnerLeaseTTL)
		if err != nil {
			return count, err
		}
	}
}
