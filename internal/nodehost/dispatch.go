package nodehost

import (
	"context"
	"errors"

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
// so the row is the result and finish leaves it alone; a row the remote host
// never finished, such as one cancelled before its claim, is finished here.
func adoptDispatchedRow(ctx context.Context, n *NodeRun, res Result) Result {
	row, err := n.Run.State.GetNode(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID)
	if err != nil || row == nil || row.Status != "done" || row.Outcome == "" {
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
