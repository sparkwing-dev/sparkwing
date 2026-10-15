package nodehost

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Debug says where a run pauses for an operator and where operators' bounce
// requests come from. The zero value pauses nowhere and polls no bounces.
type Debug struct {
	// PauseBefore and PauseAfter name the nodes to hold before they start and
	// after they end, as --sw-pause-before and --sw-pause-after.
	PauseBefore, PauseAfter []string
	// PauseOnFailure holds every node that fails, after its last attempt.
	PauseOnFailure bool
	// PauseTimeout releases a pause nobody resumes; zero means 30 minutes.
	PauseTimeout time.Duration
	// Bounces is polled while a node runs; nil turns bounce off.
	Bounces Bounces
}

// Bounces is the bounce-request surface of the store or the controller client.
type Bounces interface {
	PendingNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error)
	ConsumeNodeBounce(ctx context.Context, runID, nodeID string, seq int64, outcome string) error
}

var (
	debugPausePoll = 500 * time.Millisecond
	bouncePoll     = 5 * time.Second
)

const defaultDebugPauseTimeout = 30 * time.Minute

var errBounced = errors.New("bounced by an operator")

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

// safety: inside admission, so a bounced node keeps the lease it was admitted
// with, and outside the rest of the envelope, so a bounce restarts the node
// from its artifacts down, as killing run-node did.
func bounceLayer(cfg *Config, next Executor) Executor {
	b := cfg.Debug.Bounces
	if b == nil {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result {
		honored := map[int64]string{}
		defer bounceSweep(context.WithoutCancel(ctx), b, n, honored)
		for {
			res, req := bounceWatch(ctx, b, n, next)
			if req == nil {
				return res
			}
			outcome, kind, reason := store.BounceBounced, "node_bounced", ""
			switch {
			case ctx.Err() != nil:
				outcome, kind, reason = store.BounceMissed, "node_bounce_missed", "the run was ending"
			case res.Outcome == sparkwing.Success:
				outcome, kind, reason = store.BounceMissed, "node_bounce_missed", "the node finished before the kill landed"
			}
			writeCtx := context.WithoutCancel(ctx)
			debugEvent(writeCtx, n, kind, bouncePayload(req, reason))
			honored[req.Seq] = outcome
			if err := b.ConsumeNodeBounce(writeCtx, n.Run.ID, n.Plan.ID, req.Seq, outcome); err != nil {
				n.Run.Logger.Warn("nodehost: consume bounce", "run", n.Run.ID, "node", n.Plan.ID, "seq", req.Seq, "err", err)
			}
			if outcome == store.BounceMissed {
				return res
			}
		}
	}
}

// safety: the kill is the context's cancel, which every inner layer and the session already obey.
func bounceWatch(ctx context.Context, b Bounces, n *NodeRun, next Executor) (Result, *store.NodeBounce) {
	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// safety: buffered so the node's goroutine never blocks on a watcher that returned.
	done := make(chan Result, 1)
	go func() { done <- next(actx, n) }()
	ticker := time.NewTicker(bouncePoll)
	defer ticker.Stop()
	for {
		select {
		case res := <-done:
			return res, nil
		case <-ticker.C:
		}
		pollCtx, stop := context.WithTimeout(ctx, bouncePoll)
		req, err := b.PendingNodeBounce(pollCtx, n.Run.ID, n.Plan.ID)
		stop()
		if err != nil {
			n.Run.Logger.Debug("nodehost: poll bounce", "run", n.Run.ID, "node", n.Plan.ID, "err", err)
			continue
		}
		if req != nil {
			cancel(errBounced)
			return <-done, req
		}
	}
}

// safety: a request that arrived after the node's last poll is closed as
// missed rather than left pending to bounce a later node by the same id.
func bounceSweep(ctx context.Context, b Bounces, n *NodeRun, honored map[int64]string) {
	for range 8 {
		req, err := b.PendingNodeBounce(ctx, n.Run.ID, n.Plan.ID)
		if err != nil || req == nil {
			return
		}
		outcome, ok := honored[req.Seq]
		if !ok {
			outcome = store.BounceMissed
			debugEvent(ctx, n, "node_bounce_missed", bouncePayload(req, "the node ended before the kill was delivered"))
		}
		if err := b.ConsumeNodeBounce(ctx, n.Run.ID, n.Plan.ID, req.Seq, outcome); err != nil {
			n.Run.Logger.Warn("nodehost: consume bounce", "run", n.Run.ID, "node", n.Plan.ID, "seq", req.Seq, "err", err)
			return
		}
		honored[req.Seq] = outcome
	}
}

func bouncePayload(req *store.NodeBounce, reason string) map[string]any {
	attrs := map[string]any{
		"seq": req.Seq, "requested_by": req.RequestedBy, "requested_at": req.RequestedAt,
		"admission_lease_retained": true,
	}
	if reason != "" {
		attrs["reason"] = reason
	}
	return attrs
}
