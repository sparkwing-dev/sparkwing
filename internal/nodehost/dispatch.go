package nodehost

import (
	"cmp"
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
	dispatch, poll := cfg.Dispatch, cmp.Or(cfg.ClaimedRowPoll, defaultClaimedRowPoll)
	exec := func(ctx context.Context, n *NodeRun) Result {
		return dispatchedGenerators(ctx, cfg, n, adoptDispatchedRow(ctx, n, dispatch(ctx, n), poll))
	}
	for i := len(dispatchLayers) - 1; i >= 0; i-- {
		exec = dispatchLayers[i](cfg, exec)
	}
	return exec
}

// safety: a node's host cannot add nodes to the run, so the run host runs a
// succeeded source's generators in an eval process from the output its host
// recorded, as it does a copied source's.
func dispatchedGenerators(ctx context.Context, cfg *Config, n *NodeRun, res Result) Result {
	c := n.Plan.Closures
	if res.Outcome != sparkwing.Success || len(c.Generate) == 0 || cfg.Attempts == nil {
		return res
	}
	output, err := recordedOutput(context.WithoutCancel(ctx), n.Run.State, n.Run.ID, n.Plan.ID)
	if err != nil {
		for _, id := range c.Generate {
			res.expansions = append(res.expansions, expansion{closure: id, err: err})
		}
		return res
	}
	res.expansions = memoGenerate(ctx, cfg, n, output)
	return res
}

// safety: the remote host wrote the node's terminal row under its own claim, so
// finish leaves it alone. A row another holder still claims, as a stopped run
// host's dispatch leaves one, is that holder's: the host waits for its result
// and finishes the row only once no live claim holds it, its lease lapsed.
func adoptDispatchedRow(ctx context.Context, n *NodeRun, res Result, poll time.Duration) Result {
	readCtx := context.WithoutCancel(ctx)
	row, err := n.Run.State.GetNode(readCtx, n.Run.ID, n.Plan.ID)
	if err == nil && row != nil && liveClaim(row) && !rowTerminal(row) {
		row, err = awaitClaimedRow(ctx, n, poll)
	}
	switch {
	case err != nil || row == nil:
		return res
	case !rowTerminal(row):
		res.recorded = liveClaim(row)
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
const defaultClaimedRowPoll = 5 * time.Second

// safety: a cancelled run stops waiting and leaves the row to its holder.
func awaitClaimedRow(ctx context.Context, n *NodeRun, poll time.Duration) (*store.Node, error) {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return n.Run.State.GetNode(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID)
		case <-tick.C:
		}
		row, err := n.Run.State.GetNode(ctx, n.Run.ID, n.Plan.ID)
		if err == nil && (rowTerminal(row) || !liveClaim(row)) {
			return row, nil
		}
		if err != nil && ctx.Err() == nil {
			n.Run.Logger.Warn("nodehost: read a claimed node's row", "run", n.Run.ID, "node", n.Plan.ID, "err", err)
		}
	}
}

func rowTerminal(row *store.Node) bool { return row.Status == "done" && row.Outcome != "" }

// safety: a claim whose lease lapsed holds nothing, so a holder that died without a reaper never blocks the run.
func liveClaim(row *store.Node) bool {
	return row.Claimed && (row.LeaseExpiresAt == nil || row.LeaseExpiresAt.After(time.Now()))
}
