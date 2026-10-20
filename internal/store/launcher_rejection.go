package store

import (
	"context"
	"database/sql"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// rejectLauncherAwards fails only exact awards issued under the authenticated launcher's ancestry.
func (s *Store) rejectLauncherAwards(ctx context.Context, authority capability.AuthorityRef, fences []capability.LeaseRef) error {
	return s.inLauncherBatchTx(ctx, authority, fences, false, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
			return bindingRefusal()
		}
		root := chain[len(chain)-1].principal
		for _, fence := range fences {
			if err := launcherReservationAwardTx(ctx, tx, root.Authority, claimant.Subject, fence); err != nil {
				return err
			}
			row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: fence.Kind, UnitID: fence.ID})
			if endedLauncherPreparation(err) {
				continue
			}
			if err != nil {
				return err
			}
			if row.prefix == "" || row.holder != fence.Holder || row.generation != fence.Generation {
				continue
			}
			award, live, err := readLauncherAwardTx(ctx, tx, row.prefix, claimant, root)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			actor := Actor{Kind: ActorLauncher, Authority: authority, CapabilityPrefix: row.prefix, Holder: fence.Holder, Generation: fence.Generation}
			target := expiredUnit{team: award.Claim.Team, ref: controllerwire.NodeRef{RunID: award.Claim.RunID, NodeID: award.Claim.NodeID}, kind: fence.Kind, sourceGeneration: award.Claim.SourceGeneration}
			recovery := ownerRecovery{generation: fence.Generation, lease: sql.NullInt64{Valid: true, Int64: award.Claim.Award.LeaseExpiresAt.UnixNano()}}
			if fence.Kind == capability.UnitPlan {
				err = tx.QueryRowContext(ctx, `SELECT started_at FROM triggers WHERE team=? AND id=?`, target.team, target.ref.RunID).Scan(&recovery.started)
			} else if fence.Kind == capability.UnitEval {
				err = tx.QueryRowContext(ctx, `SELECT started_at FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration).Scan(&recovery.started)
			}
			if err != nil {
				return err
			}
			switch fence.Kind {
			case capability.UnitNode:
				_, err = transitionNodeTx(ctx, tx, target.team, NodeTransition{Ref: target.ref, To: NodeFailed, Actor: actor, Cause: CauseLaunchRejected, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "pod_rejected", MaskedError: "Cloud Pod failed verification"}}}, tx.now())
			case capability.UnitPlan:
				_, err = transitionRunTx(ctx, tx, target.team, RunTransition{RunID: target.ref.RunID, To: RunFailed, Actor: actor, Cause: RunLaunchRejected, Reason: "pod_rejected", MaskedError: "Cloud Pod failed verification"}, tx.now())
				if err == nil {
					err = writeOwnerRecoveryTx(ctx, tx, target, recovery, "failed", actor, tx.now())
				}
			case capability.UnitEval:
				err = writeEvalRecoveryTx(ctx, tx, target, recovery, "failed", tx.now())
				if err == nil {
					err = resolveExpansionLossTx(ctx, tx, target, "failed", tx.now())
				}
			}
			if err != nil {
				return err
			}
		}
		_, err = authorityChainTx(ctx, tx, authority, true)
		return err
	})
}

func checkLauncherActorTx(ctx context.Context, tx *storeTx, team Team, runID string, actor Actor) (LauncherAward, error) {
	chain, err := authorityChainTx(ctx, tx, actor.Authority, true)
	if err != nil {
		return LauncherAward{}, err
	}
	claimant := chain[0].principal.Capability
	if actor.Kind != ActorLauncher || actor.Authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
		return LauncherAward{}, bindingRefusal()
	}
	award, live, err := readLauncherAwardTx(ctx, tx, actor.CapabilityPrefix, claimant, chain[len(chain)-1].principal)
	if err != nil {
		return award, err
	}
	if !live || award.Claim.Team != team || award.Claim.RunID != runID || award.Claim.Award.Holder != actor.Holder || award.Claim.Award.Generation != actor.Generation {
		return award, bindingRefusal()
	}
	return award, nil
}
