package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// runRunHeartbeatLoop stamps last_heartbeat_at on the run row until
// ctx cancels. The controller's reaper uses these pings to detect a
// fully-orphaned orchestrator -- one whose laptop went away between
// node dispatches, so the node-claim lease reaper has nothing to
// expire. A missed ping just delays the orphan flip; correctness
// lives in the reaper's grace window. The wedge guard still bounds a
// wedged store: a "locking protocol" error or a failure streak past
// the budget stops the loop instead of re-issuing statements forever
// against a database another process has locked.
func runRunHeartbeatLoop(ctx context.Context, interval time.Duration, state StateBackend, runID string) {
	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		slog.Error("run heartbeat loop refusing to start", "run", runID, "err", err)
		return
	}
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
