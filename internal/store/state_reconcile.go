package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (t *Tenant) reconcileNode(ctx context.Context, request ReconcileRequest) (runstate.ReconciledNode, error) {
	if err := validateReconcileRequest(request); err != nil {
		return runstate.ReconciledNode{}, err
	}
	var result runstate.ReconciledNode
	err := t.s.inBlobTx(ctx, func(tx *storeTx) error {
		result = runstate.ReconciledNode{}

		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}

		if _, err := controllerOwnerTx(ctx, tx, t.team, string(request.Ref.RunID)); err != nil {
			return err
		}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{request.Actor.Authority}); err != nil {
			return err
		}

		if err := prepareStateActorTx(ctx, tx, t.team, string(request.Ref.RunID), request.Actor); err != nil {
			return err
		}

		if err := checkOwnerTx(ctx, tx, t.team, request.Actor, string(request.Ref.RunID)); err != nil {
			return err
		}
		if err := lockReconcileRunsTx(ctx, tx, t.team, request.Ref, request.Source); err != nil {
			return err
		}
		var err error
		result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
		if err != nil {
			return err
		}
		now := tx.now(ctx)
		switch {
		case result.Node.Status == runstate.StatusDone:
			result.Kind = runstate.KeepTerminal
			return nil
		case result.Node.ClaimedBy != "" && (request.Source == nil || result.Node.Status != runstate.StatusPending || result.Node.OwnerCapabilityPrefix != request.Actor.CapabilityPrefix || result.Node.ClaimedBy != request.Actor.Owner.Holder || result.Node.LeaseExpiresAt == nil || !result.Node.LeaseExpiresAt.After(now)):
			result.Kind = runstate.WaitHolder
			return nil
		}
		run, err := readRunTx(ctx, tx, t.team, string(request.Ref.RunID))
		if err != nil {
			return err
		}
		if run.CancelRequestedAt != nil || result.Node.CancelRequestedAt != nil || run.FinishedAt != nil {
			return holderRefusal()
		}
		if request.Source != nil {
			var materialized bool
			materialized, err = tryMaterializationTx(ctx, tx, t.team, request)
			if err != nil {
				return err
			}
			if materialized {
				result.Kind, result.Source = runstate.Materialize, request.Source
			}
		}
		if result.Kind == runstate.Materialize {
			result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
			return err
		}
		if request.MaterializeOnly {
			result.Kind = runstate.Dispatch
			return nil
		} else {
			if result.Node.Status != runstate.StatusPending {
				return authorityError(apierr.IllegalTransition, "job is not executable")
			}
			if result.Node.ReadyAt == nil {
				if err := releaseReconciledNodeTx(ctx, tx, t.team, request); err != nil {
					return err
				}
			}
			result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
			if err != nil {
				return err
			}
			if result.Node.ReadyAt == nil || result.Node.Status == runstate.StatusDone {
				result.Kind = runstate.Dispatch
				return nil
			}
			owner, err := tx.ResolveAuthority(ctx, request.Actor.Authority)
			if err != nil {
				return err
			}
			eligible, err := ownerExecutionEligibleTx(ctx, tx, owner, request.Ref)
			if err != nil {
				return err
			}
			if !eligible {
				result.Kind = runstate.Dispatch
				result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
				return err
			}
			claim, err := selfClaimNodeTx(ctx, tx, t.team, request.Actor, request.Ref)
			if err != nil {
				return err
			}
			result.Kind, result.Claim, result.Bearer = runstate.Execute, &claim.Award, claim.Bearer
		}
		result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
		return err
	})
	if err != nil {
		return runstate.ReconciledNode{}, err
	}
	return result, nil
}

func validateReconcileRequest(request ReconcileRequest) error {
	if request.Ref.RunID == "" || request.Ref.NodeID == "" || request.Actor.Kind != runstate.ActorRunHost {
		return authorityError(apierr.BadRequest, "reconciliation requires a job and run host")
	}
	if !slices.Contains([]runstate.SourcePolicy{runstate.SourceNone, runstate.SourceResume, runstate.SourceManualRetry, runstate.SourceMemo, runstate.SourceCoalesced}, request.Policy) {
		return authorityError(apierr.BadRequest, "unknown reconciliation source policy")
	}
	if request.Source != nil && (request.Policy == runstate.SourceNone || request.Source.Kind != request.Policy) {
		return authorityError(apierr.BadRequest, "reconciliation source does not match its policy")
	}
	return nil
}

func lockReconcileRunsTx(ctx context.Context, tx *storeTx, team Team, ref runspec.NodeRef, source *runstate.MaterializeSource) error {
	ids := []string{string(ref.RunID)}
	if source != nil {
		sourceRef, err := materializationRef(team, *source)
		if err != nil {
			return err
		}
		if source.Kind != runstate.SourceMemo {
			ids = append(ids, string(sourceRef.RunID))
		}
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if err := lockStateRunTx(ctx, tx, team, id); err != nil {
			return err
		}
	}
	return nil
}

func releaseReconciledNodeTx(ctx context.Context, tx *storeTx, team Team, request ReconcileRequest) error {
	snapshot, err := readSnapshotTx(ctx, tx, team, string(request.Ref.RunID))
	if err != nil {
		return err
	}
	graph, err := snapshot.Scheduling()
	if err != nil {
		return err
	}
	for _, decision := range Decide(graph).Nodes {
		if runspec.NodeID(decision.NodeID) == request.Ref.NodeID && decision.Kind == DecisionReady {

			_, err := transitionNodeTx(ctx, tx, team, nodeTransition{Ref: request.Ref, Actor: request.Actor, To: runstate.NodeReady, Cause: runstate.CauseRelease})
			return err
		}
	}
	return authorityError(apierr.IllegalTransition, "job dependencies are not ready for execution")
}
