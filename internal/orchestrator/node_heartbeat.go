package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runNodeHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID, nodeID string, wedgeBudget time.Duration) {
	wedge := newStoreWedgeGuard(wedgeBudget)
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if err := state.TouchNodeHeartbeat(ctx, runID, nodeID); err != nil {
		noteLostStateWrite(ctx, "touch node heartbeat", runID, err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := state.TouchNodeHeartbeat(ctx, runID, nodeID); err != nil {
				if client.IsTokenDead(err) {
					slog.Error("node heartbeat loop stopping; the controller refuses this token",
						"run", runID, "node", nodeID, "err", err)
					return
				}
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
