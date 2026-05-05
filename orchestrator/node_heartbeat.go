package orchestrator

import (
	"context"
	"time"
)

// runNodeHeartbeatLoop stamps last_heartbeat for (runID, nodeID)
// until ctx cancels. Errors are dropped: a missed ping is a UI
// annoyance, not a correctness problem; reaper uses lease_expires_at.
func runNodeHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID, nodeID string) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	// Stamp once so last_heartbeat is non-NULL before the first tick.
	_ = state.TouchNodeHeartbeat(ctx, runID, nodeID)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = state.TouchNodeHeartbeat(ctx, runID, nodeID)
		}
	}
}
