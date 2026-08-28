package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

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
