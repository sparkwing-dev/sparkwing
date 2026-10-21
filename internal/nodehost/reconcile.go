package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// WaitNodeChange waits for a committed change after a node's StateSeq. An event
// reader compares StateEvent.StateSeq for this node, not the per-run event Seq.
// It must catch up from persistence before waiting for notification.
type WaitNodeChange func(context.Context, controllerwire.NodeRef, int64) error

// FollowReconciliation waits for persisted holder changes and repeats the atomic
// store decision. It never converts elapsed wall time or an executor's previous
// result into permission to execute or finish a node.
func FollowReconciliation(ctx context.Context, reconciler Reconciler, request store.ReconcileRequest, wait WaitNodeChange) (store.ReconciledNode, error) {
	if reconciler == nil {
		return store.ReconciledNode{}, errors.New("reconciliation requires a store")
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return store.ReconciledNode{}, err
		}
		result, err := reconciler.ReconcileNode(ctx, request)
		if cause := context.Cause(ctx); cause != nil {
			return store.ReconciledNode{}, cause
		}
		if err != nil {
			return store.ReconciledNode{}, err
		}
		if result.Node.Ref != request.Ref {
			return store.ReconciledNode{}, errors.New("reconciliation changed the node identity")
		}
		switch result.Kind {
		case store.KeepTerminal, store.Materialize:
			if result.Node.Status != store.StatusDone || result.Node.Outcome == "" {
				return store.ReconciledNode{}, errors.New("reconciliation returned an unfinished result")
			}
			return result, nil
		case store.Dispatch:
			if !request.MaterializeOnly && result.Node.ReadyAt == nil || result.Claim != nil || len(result.Bearer) != 0 || result.Node.Status != store.StatusPending || result.Node.Outcome != "" || result.Node.ClaimedBy != "" {
				return store.ReconciledNode{}, errors.New("reconciliation returned an invalid dispatch miss")
			}
			return result, nil
		case store.Execute:
			if request.MaterializeOnly || !reconciledClaimMatches(result) {
				return store.ReconciledNode{}, errors.New("reconciliation returned execution without a matching claim")
			}
			return result, nil
		case store.WaitHolder:
			if wait == nil {
				return store.ReconciledNode{}, errors.New("reconciliation requires a holder event follower")
			}
			if err := wait(ctx, request.Ref, result.Node.StateSeq); err != nil {
				if cause := context.Cause(ctx); cause != nil {
					return store.ReconciledNode{}, cause
				}
				return store.ReconciledNode{}, err
			}
		default:
			return store.ReconciledNode{}, errors.New("reconciliation returned an unknown decision")
		}
	}
}

func reconciledClaimMatches(result store.ReconciledNode) bool {
	claim, row := result.Claim, result.Node
	if row.Status != store.StatusPending || row.Outcome != "" {
		return false
	}
	if claim == nil || claim.Holder == "" || claim.CapabilityPrefix == "" || claim.Generation <= 0 || claim.LeaseExpiresAt.IsZero() || row.LeaseExpiresAt == nil {
		return false
	}
	return runspec.ClaimGeneration(int64(claim.Generation)) == row.Generation && claim.Holder == row.ClaimedBy && claim.CapabilityPrefix == row.CapabilityPrefix && claim.LeaseExpiresAt.Equal(*row.LeaseExpiresAt)
}
