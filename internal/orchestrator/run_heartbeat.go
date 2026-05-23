package orchestrator

import (
	"context"
	"time"
)

// runRunHeartbeatLoop stamps last_heartbeat_at on the run row until
// ctx cancels. The controller's reaper uses these pings to detect a
// fully-orphaned orchestrator -- one whose laptop went away between
// node dispatches, so the node-claim lease reaper has nothing to
// expire. Errors are swallowed: a missed ping just delays the
// orphan flip; correctness lives in the reaper's grace window.
func runRunHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID string) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	_ = state.TouchRunHeartbeat(ctx, runID)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = state.TouchRunHeartbeat(ctx, runID)
		}
	}
}
