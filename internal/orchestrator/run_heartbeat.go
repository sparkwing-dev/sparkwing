package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runRunHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID string, wedgeBudget time.Duration) {
	wedge := newStoreWedgeGuard(wedgeBudget)
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
			if err := state.TouchRunHeartbeat(ctx, runID); err != nil {
				if client.IsTokenDead(err) {
					slog.Error("run heartbeat loop stopping; the controller refuses this token",
						"run", runID, "err", err)
					return
				}
				if terminal := wedge.fail(fmt.Sprintf("run heartbeat %s", runID), err); terminal != nil {
					slog.Error("run heartbeat loop stopping; store wedged",
						"run", runID, "err", terminal)
					return
				}
				continue
			}
			wedge.success()
		}
	}
}
