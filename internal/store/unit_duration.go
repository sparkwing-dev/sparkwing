package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (o *Operator) failExpiredClaim(ctx context.Context, demand UnitDemand, refusal *JobDurationLimitError) error {
	team, err := o.RunTeam(ctx, refusal.RunID)
	if err != nil {
		return err
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		tx.expiredClaimant = demand.CapabilityPrefix
		claimant := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{claimant}); err != nil {
			return err
		}
		change := nodeTransition{Ref: runspec.NodeRef{RunID: runspec.RunID(refusal.RunID), NodeID: runspec.NodeID(refusal.NodeID)}, To: runstate.NodeFailed, Cause: runstate.CauseDurationLimit, Actor: runstate.Actor{Kind: runstate.ActorClaimant, Authority: claimant, CapabilityPrefix: demand.CapabilityPrefix}, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: FailureJobDurationLimit, MaskedError: refusal.Error()}}}
		_, err := transitionNodeTx(ctx, tx, team, change)
		return err
	})
}

// safety: only the refused claim's own transaction ends the job, for a Cloud
// launcher that may claim it, once its standing duration limit has passed since
// its first Cloud award. A public TransitionNode caller cannot set the proof.
func checkExpiredClaimTx(ctx context.Context, tx *storeTx, team Team, row runstate.NodeRow, change nodeTransition, now time.Time) error {
	if tx.expiredClaimant == "" || tx.expiredClaimant != change.Actor.CapabilityPrefix || change.Extra.Result == nil || change.Extra.Result.Reason != FailureJobDurationLimit {
		return holderRefusal()
	}
	chain, err := teamAuthorityChainTx(ctx, tx, team, change.Actor.Authority, true, sameTeamOrCloudLauncher)
	if err != nil {
		return err
	}
	p := chain[0].principal
	if !capability.CloudLauncher(p.Capability) || change.Actor.CapabilityPrefix != change.Actor.Authority.ID {
		return holderRefusal()
	}
	unit := UnitRef{Kind: capability.UnitNode, RunID: row.Ref.RunID, NodeID: row.Ref.NodeID}
	if _, err := claimKinds(p.Capability, UnitDemand{Target: &unit}); err != nil {
		return err
	}
	if err := checkClaimantBindingTx(ctx, tx, p.Capability, unit); err != nil {
		return err
	}
	standing, err := trustStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	began, err := cloudJobBeganTx(ctx, tx, string(team), string(row.Ref.RunID), string(row.Ref.NodeID), now)
	if err != nil {
		return err
	}
	if standing.MaxJobDuration <= 0 || began.Add(standing.MaxJobDuration).After(now) {
		return holderRefusal()
	}
	return nil
}
