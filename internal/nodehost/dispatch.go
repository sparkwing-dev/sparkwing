package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: the layers a run host keeps around a dispatched node, in the
// scheduler's order; the remote host runs the rest, so a node's caches and
// artifacts land on the machine that runs it and a retry never dispatches it
// twice.
var dispatchLayers = []Layer{debugLayer, skipIfLayer, approvalsLayer, memoLayer, admissionLayer}

func dispatchChain(cfg *Config) Executor {
	dispatch := cfg.Dispatch
	exec := func(ctx context.Context, n *NodeRun) Result {
		return adoptDispatchedRow(ctx, n, dispatch(ctx, n))
	}
	for i := len(dispatchLayers) - 1; i >= 0; i-- {
		exec = dispatchLayers[i](cfg, exec)
	}
	return exec
}

// safety: the remote host wrote the node's terminal row under its own claim,
// so the row is the result and finish leaves it alone. A row another holder
// still claims, as one a stopped run host's dispatch left running is, is that
// holder's to finish: the host waits for its result, and finishes the row
// only once it is unclaimed, as the reaper leaves a claim whose lease lapsed.
func adoptDispatchedRow(ctx context.Context, n *NodeRun, res Result) Result {
	readCtx := context.WithoutCancel(ctx)
	row, err := n.Run.State.GetNode(readCtx, n.Run.ID, n.Plan.ID)
	if err == nil && row != nil && row.Claimed && !rowTerminal(row) {
		row, err = awaitClaimedRow(ctx, n)
	}
	switch {
	case err != nil || row == nil:
		return res
	case !rowTerminal(row):
		res.recorded = row.Claimed
		return res
	}
	res.Outcome, res.Reason, res.recorded = sparkwing.Outcome(row.Outcome), row.FailureReason, true
	if row.Error != "" {
		res.Err = errors.New(row.Error)
	} else if res.Outcome.OK() {
		res.Err = nil
	}
	return res
}

// perf: a holder may run for hours, so its row is read every few seconds rather than at the admission poll.
var claimedRowPoll = 5 * time.Second

// safety: a cancelled run stops waiting and leaves the row to its holder.
func awaitClaimedRow(ctx context.Context, n *NodeRun) (*store.Node, error) {
	tick := time.NewTicker(claimedRowPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return n.Run.State.GetNode(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID)
		case <-tick.C:
		}
		row, err := n.Run.State.GetNode(ctx, n.Run.ID, n.Plan.ID)
		if err == nil && (rowTerminal(row) || !row.Claimed) {
			return row, nil
		}
		if err != nil && ctx.Err() == nil {
			n.Run.Logger.Warn("nodehost: read a claimed node's row", "run", n.Run.ID, "node", n.Plan.ID, "err", err)
		}
	}
}

func rowTerminal(row *store.Node) bool { return row.Status == "done" && row.Outcome != "" }
