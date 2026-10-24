package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// WaitNodeChange waits for a committed change after a node's StateSeq. An event
// reader compares StateEvent.StateSeq for this node, not the per-run event Seq.
// It must catch up from persistence before waiting for notification.
type WaitNodeChange func(context.Context, runspec.NodeRef, int64) error

// followReconciliation waits for persisted holder changes and repeats the atomic
// store decision. It never converts elapsed wall time or an executor's previous
// result into permission to execute or finish a node.
func followReconciliation(ctx context.Context, reconciler reconciler, request store.ReconcileRequest, wait WaitNodeChange) (runstate.ReconciledNode, error) {
	if reconciler == nil {
		return runstate.ReconciledNode{}, errors.New("reconciliation requires a store")
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return runstate.ReconciledNode{}, err
		}
		result, err := reconciler.ReconcileNode(ctx, request)
		if cause := context.Cause(ctx); cause != nil {
			return runstate.ReconciledNode{}, cause
		}
		if err != nil {
			return runstate.ReconciledNode{}, err
		}
		if result.Node.Ref != request.Ref {
			return runstate.ReconciledNode{}, errors.New("reconciliation changed the node identity")
		}
		switch result.Kind {
		case runstate.KeepTerminal, runstate.Materialize:
			if result.Node.Status != runstate.StatusDone || result.Node.Outcome == "" {
				return runstate.ReconciledNode{}, errors.New("reconciliation returned an unfinished result")
			}
			return result, nil
		case runstate.Dispatch:
			if !request.MaterializeOnly && result.Node.ReadyAt == nil || result.Claim != nil || len(result.Bearer) != 0 || result.Node.Status != runstate.StatusPending || result.Node.Outcome != "" || result.Node.ClaimedBy != "" {
				return runstate.ReconciledNode{}, errors.New("reconciliation returned an invalid dispatch miss")
			}
			return result, nil
		case runstate.Execute:
			if request.MaterializeOnly || !reconciledClaimMatches(result) {
				return runstate.ReconciledNode{}, errors.New("reconciliation returned execution without a matching claim")
			}
			return result, nil
		case runstate.WaitHolder:
			if wait == nil {
				return runstate.ReconciledNode{}, errors.New("reconciliation requires a holder event follower")
			}
			if err := wait(ctx, request.Ref, result.Node.StateSeq); err != nil {
				if cause := context.Cause(ctx); cause != nil {
					return runstate.ReconciledNode{}, cause
				}
				return runstate.ReconciledNode{}, err
			}
		default:
			return runstate.ReconciledNode{}, errors.New("reconciliation returned an unknown decision")
		}
	}
}

func reconciledClaimMatches(result runstate.ReconciledNode) bool {
	claim, row := result.Claim, result.Node
	if row.Status != runstate.StatusPending || row.Outcome != "" {
		return false
	}
	if claim == nil || claim.Holder == "" || claim.CapabilityPrefix == "" || claim.Generation <= 0 || claim.LeaseExpiresAt.IsZero() || row.LeaseExpiresAt == nil {
		return false
	}
	return runspec.ClaimGeneration(int64(claim.Generation)) == row.Generation && claim.Holder == row.ClaimedBy && claim.CapabilityPrefix == row.CapabilityPrefix && claim.LeaseExpiresAt.Equal(*row.LeaseExpiresAt)
}
