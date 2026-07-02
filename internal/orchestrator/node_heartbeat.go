package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// runNodeHeartbeatLoop stamps last_heartbeat for (runID, nodeID)
// until ctx cancels. A missed ping is a UI annoyance, not a
// correctness problem; reaper uses lease_expires_at. The wedge guard
// still bounds a wedged store: a "locking protocol" error or a
// failure streak past the budget stops the loop instead of re-issuing
// statements forever against a database another process has locked.
func runNodeHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID, nodeID string) {
	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		slog.Error("node heartbeat loop refusing to start", "run", runID, "node", nodeID, "err", err)
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	_ = state.TouchNodeHeartbeat(ctx, runID, nodeID)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := state.TouchNodeHeartbeat(ctx, runID, nodeID); err != nil {
				if terminal := wedge.fail(fmt.Sprintf("node heartbeat %s/%s", runID, nodeID), err); terminal != nil {
					slog.Error("node heartbeat loop stopping; store wedged",
						"run", runID, "node", nodeID, "err", terminal)
					return
				}
				continue
			}
			wedge.success()
		}
	}
}
