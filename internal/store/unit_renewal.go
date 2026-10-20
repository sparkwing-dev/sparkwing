package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Store) renewUnit(ctx context.Context, prefix string, executing bool) (LeaseBeat, error) {
	var beat LeaseBeat
	err := s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}, prefix); err != nil {
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
			if err := tenant.renewNodeClaimTx(ctx, tx, controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, actor, &beat); err != nil {
				return err
			}
			if executing && !beat.CancelRequested {
				row, err := readNodeTx(ctx, tx, tenant.team, controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
				if err != nil {
					return err
				}
				if row.Status != StatusRunning {
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
		return computeUnitBillingTx(ctx, tx, tenant.team, unit, actor.Generation, 0, executing || c.Billing != nil && c.Billing.ExecutorClass == TierCloud)
	})
	if err != nil {
		return LeaseBeat{}, err
	}
	return beat, nil
}

func renewalActor(c capability.Capability) (UnitRef, Actor, error) {
	if c.Audience != capability.Controller || c.Delivery != capability.DeliveryDirect || c.Team == "" || c.Binding.Holder == "" || c.Binding.Generation < 0 || c.Unit != capability.UnitEval && c.Binding.Generation == 0 {
		return UnitRef{}, Actor{}, holderRefusal()
	}
	unit, err := decodeAwardUnit(c.Unit, c.UnitID)
	if err != nil {
		return UnitRef{}, Actor{}, err
	}
	if unit.RunID != c.Binding.RunID || unit.NodeID != c.Binding.NodeID {
		return UnitRef{}, Actor{}, holderRefusal()
	}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	grant, binding := capability.NodeReport, capability.BindingNode
	switch c.Unit {
	case capability.UnitTrigger, capability.UnitPlan:
		actor.Kind = ActorRunHost
		actor.Owner = OwnerLeaseRef{Kind: OwnerTrigger, RunID: unit.RunID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
		binding = capability.BindingRun
		if c.Unit == capability.UnitTrigger {
			grant, binding = capability.TriggerHost, capability.BindingTrigger
		}
	case capability.UnitEval:
		lease := c.Binding.Expansion
		if lease == nil || lease.Kind != capability.UnitEval || lease.ID != c.UnitID || lease.Holder != c.Binding.Holder || lease.Generation <= 0 || c.Binding.Generation != unit.SourceGeneration {
			return UnitRef{}, Actor{}, holderRefusal()
		}
		grant = capability.ExpansionSubmit
		actor.Generation = lease.Generation
		actor.Owner = OwnerLeaseRef{Kind: OwnerEval, RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Holder: lease.Holder, Generation: lease.Generation}
	case capability.UnitNode:
		if c.Binding.Expansion != nil {
			return UnitRef{}, Actor{}, holderRefusal()
		}
	default:
		return UnitRef{}, Actor{}, holderRefusal()
	}
	if c.Binding.Kind != binding || !slices.Contains(c.Grants, grant) {
		return UnitRef{}, Actor{}, holderRefusal()
	}
	return unit, actor, nil
}

func renewEvalTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, actor Actor, beat *LeaseBeat) error {
	request := ExpansionSubmission{Source: controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, SourceGeneration: unit.SourceGeneration, Actor: actor}
	if err := expansionAuthorityTx(ctx, tx, team, request, tx.now()); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, team, unit.RunID); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, team, unit.RunID)
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
	if source.Generation != unit.SourceGeneration || source.Status != StatusDone || !scheduleSucceeded(string(source.Outcome)) || source.Expansion != ExpansionExpected && run.CancelRequestedAt == nil {
		return holderRefusal()
	}
	if err := expansionAuthorityTx(ctx, tx, team, request, tx.now()); err != nil {
		return err
	}
	*beat = LeaseBeat{CancelRequested: run.CancelRequestedAt != nil, CancelReason: run.CancelReason}
	funded, err := renewUnitBillingTx(ctx, tx, team, unit, actor.Generation, beat)
	if err != nil {
		return err
	}
	expiry, err := leaseBoundTx(ctx, tx, actor.CapabilityPrefix, tx.now(), funded)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE eval_units SET lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND generation=? AND capability_prefix=?`, expiry.UnixNano(), team, unit.RunID, unit.NodeID, unit.SourceGeneration, actor.Generation, actor.CapabilityPrefix)
	if err != nil {
		return err
	}
	beat.ExpiresAt = expiry
	return observeUnitTx(ctx, tx, actor.CapabilityPrefix)
}
