package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (s *Store) renewUnit(ctx context.Context, prefix string, executing bool) (runstate.LeaseBeat, error) {
	var beat runstate.LeaseBeat
	err := s.inTx(ctx, func(tx *storeTx) error {
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}, prefix); err != nil {
			return err
		}
		principal, err := tx.ResolveAuthority(ctx, ref)
		if err != nil {
			return err
		}
		c := principal.Capability
		unit, actor, err := renewalActor(c)
		if err != nil {
			return err
		}
		tenant := &Tenant{s: s, team: Team(c.Team)}
		switch unit.Kind {
		case capability.UnitNode:
			if err := tenant.renewNodeClaimTx(ctx, tx, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, actor, &beat); err != nil {
				return err
			}
			if executing && !beat.CancelRequested {
				row, err := readNodeTx(ctx, tx, tenant.team, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
				if err != nil {
					return err
				}
				if row.Status != runstate.StatusRunning {
					return holderRefusal()
				}
			}
			return nil
		case capability.UnitEval:
			if err := renewEvalTx(ctx, tx, tenant.team, unit, actor, &beat); err != nil {
				return err
			}
		default:
			if err := tenant.renewOwnerTx(ctx, tx, actor, &beat); err != nil {
				return err
			}
		}
		if executing && !beat.CancelRequested {
			if err := startOwnerUnitTx(ctx, tx, tenant.team, unit, actor); err != nil {
				return err
			}
		}
		if beat.CancelRequested {
			return nil
		}
		return computeUnitBillingTx(ctx, tx, tenant.team, unit, int64(actor.Generation), 0, executing || c.Billing != nil && c.Billing.ExecutorClass == TierCloud)
	})
	if err != nil {
		return runstate.LeaseBeat{}, err
	}
	return beat, nil
}

func renewalActor(c capability.Capability) (UnitRef, runstate.Actor, error) {
	if c.Audience != capability.Controller || c.Delivery != capability.DeliveryDirect || c.Team == "" || c.Binding.Holder == "" || c.Binding.Generation < 0 || c.Unit != capability.UnitEval && c.Binding.Generation == 0 {
		return UnitRef{}, runstate.Actor{}, holderRefusal()
	}
	unit, err := decodeAwardUnit(c.Unit, c.UnitID)
	if err != nil {
		return UnitRef{}, runstate.Actor{}, err
	}
	if string(unit.RunID) != c.Binding.RunID || string(unit.NodeID) != c.Binding.NodeID {
		return UnitRef{}, runstate.Actor{}, holderRefusal()
	}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID), Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}
	grant, binding := capability.NodeReport, capability.BindingNode
	switch c.Unit {
	case capability.UnitTrigger, capability.UnitPlan:
		actor.Kind = runstate.ActorRunHost
		actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: unit.RunID, Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}
		binding = capability.BindingRun
		if c.Unit == capability.UnitTrigger {
			grant, binding = capability.TriggerHost, capability.BindingTrigger
		}
	case capability.UnitEval:
		lease := c.Binding.Expansion
		if lease == nil || lease.Kind != capability.UnitEval || lease.ID != c.UnitID || lease.Holder != c.Binding.Holder || lease.Generation <= 0 || c.Binding.Generation != unit.SourceGeneration {
			return UnitRef{}, runstate.Actor{}, holderRefusal()
		}
		grant = capability.ExpansionSubmit
		actor.Generation = runspec.ClaimGeneration(lease.Generation)
		actor.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: runspec.ClaimGeneration(unit.SourceGeneration), Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}
	case capability.UnitNode:
		if c.Binding.Expansion != nil {
			return UnitRef{}, runstate.Actor{}, holderRefusal()
		}
	default:
		return UnitRef{}, runstate.Actor{}, holderRefusal()
	}
	if c.Binding.Kind != binding || !slices.Contains(c.Grants, grant) {
		return UnitRef{}, runstate.Actor{}, holderRefusal()
	}
	return unit, actor, nil
}

func renewEvalTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, actor runstate.Actor, beat *runstate.LeaseBeat) error {
	request := ExpansionSubmission{Source: runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, SourceGeneration: runspec.ClaimGeneration(unit.SourceGeneration), Actor: actor}
	if err := expansionAuthorityTx(ctx, tx, team, request); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, team, string(unit.RunID)); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, team, string(unit.RunID))
	if err != nil {
		return err
	}
	if run.FinishedAt != nil || run.Dispatch != "controller" {
		return holderRefusal()
	}
	source, err := readNodeTx(ctx, tx, team, request.Source)
	if err != nil {
		return err
	}
	if source.Generation != runspec.ClaimGeneration(unit.SourceGeneration) || !evaluationSourceCurrent(source) && (run.CancelRequestedAt == nil || source.Status != runstate.StatusDone || !scheduleSucceeded(string(source.Outcome))) {
		return holderRefusal()
	}
	*beat = runstate.LeaseBeat{CancelRequested: run.CancelRequestedAt != nil, CancelReason: run.CancelReason}
	funded, err := renewUnitBillingTx(ctx, tx, team, unit, int64(actor.Generation), beat)
	if err != nil {
		return err
	}
	if err := expansionAuthorityTx(ctx, tx, team, request); err != nil {
		return err
	}
	expiry, err := leaseBoundTx(ctx, tx, actor.CapabilityPrefix, tx.now(ctx), funded)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `UPDATE eval_units SET lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=? AND capability_prefix=?`, expiry.UnixNano(), team, unit.RunID, unit.NodeID, unit.SourceGeneration, actor.Generation, actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	beat.ExpiresAt = expiry
	if err := renewCapacityTx(ctx, tx, team, string(unit.RunID), string(unit.NodeID), expiry); err != nil {
		return err
	}
	return observeUnitTx(ctx, tx, actor.CapabilityPrefix)
}
