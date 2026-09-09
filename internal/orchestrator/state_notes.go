package orchestrator

import (
	"context"
	"log/slog"
)

// safety: the orchestrator's one sanctioned place to lose a state-write error.
// Every caller is already leaving with the outcome the write was meant to record,
// so returning would replace a real outcome with a storage complaint, and dropping
// it silently would leave stored state disagreeing with what the process reported.
func noteLostStateWrite(ctx context.Context, write, runID string, err error) {
	if err == nil {
		return
	}
	slog.WarnContext(ctx, "state write failed", "write", write, "run", runID, "err", err)
}

// safety: an event records something that already happened, so a store that
// refuses one must not change what the run does.
func noteEvent(ctx context.Context, state StateBackend, runID, nodeID, kind string, payload []byte) {
	noteLostStateWrite(ctx, "append event "+kind+" on node "+nodeID, runID,
		state.AppendEvent(ctx, runID, nodeID, kind, payload))
}
