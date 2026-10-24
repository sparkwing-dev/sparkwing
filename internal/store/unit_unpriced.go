package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (o *Operator) failUnpricedClaim(ctx context.Context, demand UnitDemand, refusal *UnpricedCPUClassError) error {
	team, err := o.RunTeam(ctx, refusal.RunID)
	if err != nil {
		return err
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		tx.unpricedDemand = &demand
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}}); err != nil {
			return err
		}
		change := nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(refusal.RunID), NodeID: runspec.NodeID(refusal.NodeID)}, To: runstate.NodeFailed, Cause: runstate.CauseUnpriced, Actor: runstate.Actor{Kind: runstate.ActorClaimant, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}, CapabilityPrefix: demand.CapabilityPrefix}, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: FailureUnpricedCPUClass, MaskedError: refusal.Error()}}}

		_, err := transitionNodeTx(ctx, tx, team, change)
		return err
	})
}

func checkUnpricedClaimTx(ctx context.Context, tx *storeTx, row runstate.NodeRow, change nodeTransition) error {
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
	if verdict := match.Evaluate(profile, requirements); !verdict.OK() {
		return holderRefusal()
	}
	if _, err := unitBillingPriceTx(ctx, tx, row.Execution.Requires.Value, profile); !errors.Is(err, ErrUnpricedCPUClass) {
		return holderRefusal()
	}
	return nil
}
