package nodehost

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Debug selects the operator pauses for a run.
type Debug struct {
	// PauseBefore and PauseAfter name the nodes to hold before they start and
	// after they end, as --sw-pause-before and --sw-pause-after.
	PauseBefore, PauseAfter []string
	// PauseOnFailure holds every node that fails, after its last attempt.
	PauseOnFailure bool
	// PauseTimeout releases a pause nobody resumes; zero means 30 minutes.
	PauseTimeout time.Duration
}

var debugPausePoll = 500 * time.Millisecond

const defaultDebugPauseTimeout = 30 * time.Minute

// safety: outermost, so pause-before holds an approval gate before it waits
// and comes before skip_if, as in the dispatcher. A recovery node never
// pauses, and an approval gate never pauses after.
func debugLayer(cfg *Config, next Executor) Executor {
	d := cfg.Debug
	if len(d.PauseBefore) == 0 && len(d.PauseAfter) == 0 && !d.PauseOnFailure {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result {
		if n.Plan.OnFailureOf != "" {
			return next(ctx, n)
		}
		if slices.Contains(d.PauseBefore, n.Plan.ID) && !debugPause(ctx, d, n, store.PauseReasonBefore) {
			return Result{Outcome: sparkwing.Cancelled, Err: errors.New("ctx-cancelled")}
		}
		res := next(ctx, n)
		if n.Plan.Approval != nil || ctx.Err() != nil {
			return res
		}
		reason := ""
		switch {
		case slices.Contains(d.PauseAfter, n.Plan.ID):
			reason = store.PauseReasonAfter
		case d.PauseOnFailure && res.Outcome == sparkwing.Failed:
			reason = store.PauseReasonOnFailure
		}
		if reason != "" && !debugPause(ctx, d, n, reason) {
			return Result{Outcome: sparkwing.Cancelled, Err: errors.New("ctx-cancelled")}
		}
		return res
	}
}

// safety: a pause row the store refuses is logged and skipped, so a debug flag never wedges a run.
func debugPause(ctx context.Context, d Debug, n *NodeRun, reason string) bool {
	run, id := n.Run, n.Plan.ID
	writeCtx := context.WithoutCancel(ctx)
	timeout := d.PauseTimeout
	if timeout <= 0 {
		timeout = defaultDebugPauseTimeout
	}
	now := time.Now()
	p := store.DebugPause{RunID: run.ID, NodeID: id, Reason: reason, PausedAt: now, ExpiresAt: now.Add(timeout)}
	if err := run.State.CreateDebugPause(writeCtx, p); err != nil {
		run.Logger.Error("nodehost: debug pause", "run", run.ID, "node", id, "reason", reason, "err", err)
		return true
	}
	debugSetStatus(writeCtx, n, "paused")
	debugEvent(writeCtx, n, "node_paused", map[string]any{"reason": reason, "expires_at": p.ExpiresAt})

	ticker := time.NewTicker(debugPausePoll)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(p.ExpiresAt))
	defer deadline.Stop()
	for {
		active, err := run.State.GetActiveDebugPause(ctx, run.ID, id)
		if ctx.Err() != nil {
			return false
		}
		if err != nil || active == nil || active.ReleasedAt != nil {
			break
		}
		if !time.Now().Before(active.ExpiresAt) {
			if err := run.State.ReleaseDebugPause(writeCtx, run.ID, id, "orchestrator", store.PauseReleaseTimeout); err != nil {
				run.Logger.Warn("nodehost: release expired debug pause", "run", run.ID, "node", id, "err", err)
			}
			debugEvent(writeCtx, n, "node_paused_timeout", nil)
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		case <-deadline.C:
		}
	}
	// safety: pending is safe both ways: the chain's StartNode promotes it and the scheduler's finish overwrites it.
	debugSetStatus(writeCtx, n, "pending")
	debugEvent(writeCtx, n, "node_resumed", nil)
	return true
}

func debugSetStatus(ctx context.Context, n *NodeRun, status string) {
	if err := n.Run.State.SetNodeStatus(ctx, n.Run.ID, n.Plan.ID, status); err != nil {
		n.Run.Logger.Warn("nodehost: debug pause status", "run", n.Run.ID, "node", n.Plan.ID, "status", status, "err", err)
	}
}

func debugEvent(ctx context.Context, n *NodeRun, kind string, attrs map[string]any) {
	hostEventJSON(ctx, n.Run, n.Plan.ID, kind, attrs)
}
