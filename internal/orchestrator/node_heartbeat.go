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
// failure streak past wedgeBudget stops the loop instead of
// re-issuing statements forever against a database another process
// has locked. The caller resolves (and error-checks) the budget
// before spawning the loop.
func runNodeHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID, nodeID string, wedgeBudget time.Duration) {
	wedge := newStoreWedgeGuard(wedgeBudget)
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
