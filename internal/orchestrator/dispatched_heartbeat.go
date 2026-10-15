package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var errClaimAbandoned = errors.New("run-node: the controller no longer renews this node's claim, so the node stopped")

// safety: the controller refuses a renewal when the team's credits run out,
// the claim was reaped, or the node was cancelled, and a pod that kept going
// would bill compute until the Job deadline. A controller unreachable for a
// whole lease has let the claim lapse, so the pod stops then too.
func heartbeatDispatchedClaim(
	ctx context.Context,
	ctrl *client.Client,
	runID, nodeID string,
	fence store.NodeClaimFence,
	lease time.Duration,
	abandon context.CancelCauseFunc,
	logger *slog.Logger,
) {
	ctx = store.WithNodeClaimFence(ctx, fence)
	lastOK := time.Now()
	renew := func() (stop bool) {
		err := ctrl.HeartbeatNodeClaim(ctx, runID, nodeID, fence.HolderID, lease, nil)
		switch {
		case err == nil:
			lastOK = time.Now()
		case ctx.Err() != nil:
			return true
		case errors.Is(err, store.ErrLockHeld) || client.IsTokenDead(err):
			logger.Error("run-node: the controller refused the claim renewal; stopping the node",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID)
			abandon(fmt.Errorf("%w: %w", errClaimAbandoned, err))
			return true
		case time.Since(lastOK) >= lease:
			logger.Error("run-node: the controller was unreachable for the whole claim lease; stopping the node",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID, "err", err)
			abandon(fmt.Errorf("%w: %w", errClaimAbandoned, err))
			return true
		default:
			logger.Warn("run-node: claim heartbeat failed",
				"run_id", runID, "node_id", nodeID, "holder_id", fence.HolderID, "err", err)
		}
		return false
	}
	// safety: the pod's first renewal is where the controller starts billing a
	// dispatched node, so it goes out as the pod starts rather than one
	// interval later; the fetch and compile after it are the customer's work.
	if renew() {
		return
	}
	t := time.NewTicker(store.DispatchedHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if renew() {
				return
			}
		}
	}
}
