package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (s *Store) failUnpricedClaim(ctx context.Context, demand UnitDemand, refusal *UnpricedCPUClassError) error {
	team, err := s.AsOperator().RunTeam(ctx, refusal.RunID)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *storeTx) error {
		tx.unpricedDemand = &demand
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}}); err != nil {
			return err
		}
		change := NodeTransition{Ref: NodeRef{refusal.RunID, refusal.NodeID}, To: NodeFailed, Cause: CauseUnpriced, Actor: Actor{Kind: ActorClaimant, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, CapabilityPrefix: demand.CapabilityPrefix}, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: FailureUnpricedCPUClass, MaskedError: refusal.Error()}}}
		_, err := transitionNodeTx(ctx, tx, team, change, tx.now())
		return err
	})
}

func checkUnpricedClaimTx(ctx context.Context, tx *storeTx, row NodeRow, change NodeTransition) error {
	if tx.unpricedDemand == nil || tx.unpricedDemand.CapabilityPrefix != change.Actor.CapabilityPrefix {
		return holderRefusal()
	}
	if change.Extra.Result == nil || change.Extra.Result.Reason != FailureUnpricedCPUClass {
		return holderRefusal()
	}
	chain, err := authorityChainTx(ctx, tx, change.Actor.Authority, true)
	if err != nil {
		return err
	}
	p := chain[0].principal
	unit := UnitRef{Kind: capability.UnitNode, RunID: row.Ref.RunID, NodeID: row.Ref.NodeID}
	if _, err := claimKinds(p.Capability, UnitDemand{Target: &unit}); err != nil {
		return err
	}
	if !meteredPlacement(p.Capability, chain[len(chain)-1].principal.Capability) {
		return holderRefusal()
	}
	if err := checkClaimantBindingTx(ctx, tx, p.Capability, unit); err != nil {
		return err
	}
	profile, requirements, err := matchingInputsTx(ctx, tx, *tx.unpricedDemand, unit)
	if err != nil {
		return err
	}
	if verdict := match.Evaluate(profile, requirements); !verdict.OK() && verdict.Reason != match.ReasonShape {
		return holderRefusal()
	}
	if _, err := unitBillingPriceTx(ctx, tx, matchingResources(row.Execution.Resources.Value)); !errors.Is(err, ErrUnpricedCPUClass) {
		return holderRefusal()
	}
	return nil
}
