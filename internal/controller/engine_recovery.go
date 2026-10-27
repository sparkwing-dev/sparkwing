package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const controllerRecoveryBatch = 500

func (s *Server) reconcileOwnedRuns(ctx context.Context) {
	if _, err := reconcileControllerRuns(ctx, s.operator); err != nil {
		s.logger.Error("controller run recovery failed", "err", err)
	}
}

func reconcileControllerRuns(ctx context.Context, operator store.OperatorStore) (int, error) {
	count, after := 0, ""
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		page, err := operator.ReconcileControllerRuns(ctx, after, controllerRecoveryBatch)
		count += len(page)
		if err != nil || len(page) < controllerRecoveryBatch {
			return count, err
		}
		after = page[len(page)-1].RunID
	}
}
