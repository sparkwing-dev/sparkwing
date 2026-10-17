package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func (t *Tenant) reconcileNode(ctx context.Context, request ReconcileRequest) (ReconciledNode, error) {
	if err := validateReconcileRequest(request); err != nil {
		return ReconciledNode{}, err
	}
	var result ReconciledNode
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if _, err := controllerOwnerTx(ctx, tx, t.team, request.Ref.RunID, tx.now()); err != nil {
			return err
		}
		if err := prepareStateActorTx(ctx, tx, t.team, request.Ref.RunID, request.Actor, tx.now()); err != nil {
			return err
		}
		if err := checkOwnerTx(ctx, tx, t.team, request.Actor, request.Ref.RunID, tx.now()); err != nil {
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
		switch {
		case result.Node.Status == StatusDone:
			result.Kind = KeepTerminal
			return nil
		case result.Node.ClaimedBy != "" && (request.Source == nil || result.Node.Status != StatusPending || result.Node.OwnerCapabilityPrefix != request.Actor.CapabilityPrefix || result.Node.ClaimedBy != request.Actor.Owner.Holder || result.Node.LeaseExpiresAt == nil || !result.Node.LeaseExpiresAt.After(tx.now())):
			result.Kind = WaitHolder
			return nil
		}
		run, err := readRunTx(ctx, tx, t.team, request.Ref.RunID)
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
				result.Kind, result.Source = Materialize, request.Source
			}
		}
		if result.Kind == Materialize {
			result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
			return err
		}
		if request.MaterializeOnly {
			result.Kind = Dispatch
			return nil
		} else {
			if result.Node.Status != StatusPending {
				return authorityError(apierr.IllegalTransition, "job is not executable")
			}
			if result.Node.ReadyAt == nil {
				if err := releaseReconciledNodeTx(ctx, tx, t.team, request); err != nil {
					return err
				}
			}
			owner, err := tx.ResolveAuthority(ctx, request.Actor.Authority)
			if err != nil {
				return err
			}
			eligible, err := ownerExecutionEligibleTx(ctx, tx, owner, request.Ref, request.Reserved)
			if err != nil {
				return err
			}
			if !eligible {
				result.Kind = Dispatch
				result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
				return err
			}
			claim, err := selfClaimNodeTx(ctx, tx, t.team, request.Actor, request.Ref, request.Reserved)
			if err != nil {
				return err
			}
			result.Kind, result.Claim, result.Bearer = Execute, &claim.Award, claim.Bearer
		}
		result.Node, err = readNodeTx(ctx, tx, t.team, request.Ref)
		return err
	})
	if err != nil {
		return ReconciledNode{}, err
	}
	return result, nil
}

func validateReconcileRequest(request ReconcileRequest) error {
	if request.Ref.RunID == "" || request.Ref.NodeID == "" || request.Actor.Kind != ActorRunHost {
		return authorityError(apierr.BadRequest, "reconciliation requires a job and run host")
	}
	if !slices.Contains([]SourcePolicy{SourceNone, SourceResume, SourceManualRetry, SourceMemo, SourceCoalesced}, request.Policy) {
		return authorityError(apierr.BadRequest, "unknown reconciliation source policy")
	}
	if request.Source != nil && (request.Policy == SourceNone || request.Source.Kind != request.Policy) {
		return authorityError(apierr.BadRequest, "reconciliation source does not match its policy")
	}
	return nil
}

func lockReconcileRunsTx(ctx context.Context, tx *storeTx, team Team, ref NodeRef, source *MaterializeSource) error {
	ids := []string{ref.RunID}
	if source != nil {
		sourceRef, err := materializationRef(team, *source)
		if err != nil {
			return err
		}
		if source.Kind != SourceMemo {
			ids = append(ids, sourceRef.RunID)
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
	snapshot, err := readSnapshotTx(ctx, tx, team, request.Ref.RunID)
	if err != nil {
		return err
	}
	graph, err := snapshot.Scheduling()
	if err != nil {
		return err
	}
	for _, decision := range Decide(graph).Nodes {
		if decision.NodeID == request.Ref.NodeID && decision.Kind == DecisionReady {
			_, err := transitionNodeTx(ctx, tx, team, NodeTransition{Ref: request.Ref, Actor: request.Actor, To: NodeReady, Cause: CauseRelease}, tx.now())
			return err
		}
	}
	return authorityError(apierr.IllegalTransition, "job dependencies are not ready for execution")
}
