package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// rejectLauncherAwards fails only exact awards issued under the authenticated launcher's ancestry.
func (s *Store) rejectLauncherAwards(ctx context.Context, authority capability.AuthorityRef, fences []capability.LeaseRef) error {
	return s.failLauncherAwards(ctx, authority, fences, "pod_rejected")
}

func (s *Store) failLauncherAwards(ctx context.Context, authority capability.AuthorityRef, fences []capability.LeaseRef, reason string) error {
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
			row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: fence.Kind, UnitID: fence.ID})
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
			message := "Cloud Pod failed verification"
			if reason == capability.CloudCapacityUnconfigured {
				if !capability.CloudLauncher(claimant) || award.Capability.Delivery != capability.DeliveryBootstrap || award.Capability.ExecutionHost != "" {
					return bindingRefusal()
				}
				message = fmt.Sprintf("%s (Support reference: %s)", capability.CloudCapacityUnconfiguredMessage, award.Claim.RunID)
			}
			actor := runstate.Actor{Kind: runstate.ActorLauncher, Authority: authority, CapabilityPrefix: row.prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(award.Claim.RunID), NodeID: runspec.NodeID(award.Claim.NodeID), Holder: fence.Holder, Generation: runspec.ClaimGeneration(fence.Generation)}}
			target := expiredUnit{team: award.Claim.Team, ref: runspec.NodeRef{RunID: runspec.RunID(award.Claim.RunID), NodeID: runspec.NodeID(award.Claim.NodeID)}, kind: fence.Kind, sourceGeneration: award.Claim.SourceGeneration}
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

				_, err = transitionNodeTx(ctx, tx, target.team, nodeTransition{Ref: target.ref, To: runstate.NodeFailed, Actor: actor, Cause: runstate.CauseLaunchRejected, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: reason, MaskedError: message}}})
			case capability.UnitPlan:
				_, err = transitionRunTx(ctx, tx, target.team, runstate.RunTransition{RunID: target.ref.RunID, To: runstate.RunFailed, Actor: actor, Cause: runstate.RunLaunchRejected, Reason: reason, MaskedError: message})
				if err == nil {
					err = writeOwnerRecoveryTx(ctx, tx, target, recovery, "failed", actor, tx.now(ctx))
				}
			case capability.UnitEval:
				err = writeEvalRecoveryTx(ctx, tx, target, recovery, "failed", tx.now(ctx))
				if err == nil {
					if reason == capability.CloudCapacityUnconfigured {
						err = resolveExpansionFailureTx(ctx, tx, target, runstate.ExpansionFailed, &ExpansionFailure{Code: apierr.Unavailable, MaskedError: message}, tx.now(ctx))
						if err == nil {
							_, err = tx.execContext(ctx, `UPDATE nodes SET failure_reason=?,error=? WHERE team=? AND run_id=? AND node_id=? AND generation=?`, reason, message, target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration)
						}
					} else {
						err = resolveExpansionLossTx(ctx, tx, target, "failed", tx.now(ctx))
					}
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

func checkLauncherActorTx(ctx context.Context, tx *storeTx, team Team, runID string, actor runstate.Actor) (LauncherAward, error) {
	chain, err := teamAuthorityChainTx(ctx, tx, team, actor.Authority, true, sameTeamOrCloudLauncher)
	if err != nil {
		return LauncherAward{}, err
	}
	claimant := chain[0].principal.Capability
	if actor.Kind != runstate.ActorLauncher || actor.Authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
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
