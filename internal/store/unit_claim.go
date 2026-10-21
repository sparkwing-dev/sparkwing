package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (o *Operator) claimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	var out UnitClaim
	var waiting error
	var selected *UnitRef
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		out, waiting, selected = UnitClaim{}, nil, nil
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		claimant := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{claimant}); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, claimant)
		if err != nil {
			return err
		}
		kinds, err := claimKinds(p.Capability, demand)
		if err != nil {
			return err
		}
		if !validMatchingResources(demand.Capacity) || !validMatchingResources(demand.Available) {
			return authorityError(apierr.BadRequest, "invalid claim resources")
		}
		unit := demand.Target
		if unit == nil {
			queued, err := queuedUnitTx(ctx, tx, p.Capability, demand, kinds)
			if errors.Is(err, ErrNotFound) {
				waiting = err
				return nil
			}
			if err != nil {
				return err
			}
			if capability.CloudLauncher(p.Capability) {
				selected = &queued
				return nil
			}
			unit = &queued
		}
		out, err = tx.operator.claimUnitTx(ctx, tx, demand, *unit)
		if err == errRankingHold {
			waiting, err = err, nil
		}
		return err
	})
	if err == nil && selected != nil {
		demand.Target = selected
		return o.claimUnit(ctx, demand)
	}
	if err == nil && waiting != nil {
		return UnitClaim{}, waiting
	}
	if err != nil {
		var unpriced *UnpricedCPUClassError
		if errors.As(err, &unpriced) && unpriced.RunID != "" && unpriced.NodeID != "" {
			err = errors.Join(err, o.failUnpricedClaim(ctx, demand, unpriced))
		}
		return UnitClaim{}, err
	}
	return out, nil
}

// safety: a held claim commits its transaction, so the ask it recorded survives; nothing else was written by then.
var errRankingHold = authorityError(apierr.LockHeld, "work is waiting for a preferred runner")

func lockClaimControllerTx(ctx context.Context, tx *storeTx) error {
	var holder string
	err := tx.QueryRowContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner'`+tx.forKeyShare()).Scan(&holder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func claimKinds(c capability.Capability, demand UnitDemand) ([]capability.UnitKind, error) {
	if c.Audience != capability.Controller || c.Subject.ID == "" {
		return nil, authorityError(apierr.Forbidden, "claim requires controller authority")
	}
	kinds := slices.Clone(demand.Kinds)
	if demand.Target != nil && len(kinds) == 0 {
		kinds = []capability.UnitKind{demand.Target.Kind}
	}
	if len(kinds) == 0 {
		return nil, authorityError(apierr.BadRequest, "claim requires unit kinds")
	}
	seen := map[capability.UnitKind]bool{}
	for _, kind := range kinds {
		if !slices.Contains([]capability.UnitKind{capability.UnitTrigger, capability.UnitNode, capability.UnitPlan, capability.UnitEval}, kind) || seen[kind] {
			return nil, authorityError(apierr.BadRequest, "claim kinds must be known and unique")
		}
		seen[kind] = true
	}
	if demand.Target != nil && !seen[demand.Target.Kind] {
		return nil, authorityError(apierr.BadRequest, "claim target is outside requested kinds")
	}
	for _, kind := range kinds {
		grant := capability.NodesClaim
		if kind == capability.UnitTrigger || kind == capability.UnitPlan {
			grant = capability.TriggersClaim
		}
		unbound := c.Binding.Kind == capability.BindingNone || c.Binding.Kind == capability.BindingPush
		allowed := slices.Contains(c.Grants, grant) && (kind == capability.UnitNode || kind == capability.UnitTrigger || unbound)
		if launcherClaimant(c) {
			allowed = kind == capability.UnitPlan || kind == capability.UnitEval || kind == capability.UnitNode
		}
		if c.Purpose == capability.PurposeHere {
			allowed = kind == capability.UnitTrigger && demand.Target != nil && demand.Target.RunID == c.Binding.RunID && slices.Contains(c.Grants, capability.TriggerHost)
		}
		if !allowed {
			return nil, authorityError(apierr.Forbidden, "claimant lacks the unit claim grant")
		}
	}
	return kinds, nil
}

func (op *Operator) claimUnitTx(ctx context.Context, tx *storeTx, demand UnitDemand, unit UnitRef) (_ UnitClaim, err error) {
	defer func() {
		var unpriced *UnpricedCPUClassError
		if errors.As(err, &unpriced) {
			unpriced.RunID, unpriced.NodeID = unit.RunID, unit.NodeID
		}
	}()

	if err := validateClaimUnit(unit); err != nil {
		return UnitClaim{}, err
	}
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil {
		return UnitClaim{}, err
	}
	if _, err = claimKinds(p.Capability, UnitDemand{Target: &unit}); err != nil {
		return UnitClaim{}, err
	}
	var team Team
	if err = tx.QueryRowContext(ctx, `SELECT team FROM runs WHERE id=?`, unit.RunID).Scan(&team); err != nil {
		return UnitClaim{}, err
	}
	named := demand
	named.Kinds = nil
	standing, err := unitEligibilityTx(ctx, tx, team, unit, named, eligibilityChecks{lock: true, fit: true, claim: true})
	if err != nil {
		return UnitClaim{}, err
	}
	if !standing.ok() {
		return UnitClaim{}, standing.refused
	}
	profile, requirements := standing.profile, standing.demand
	if err = checkClaimantBindingTx(ctx, tx, p.Capability, unit); err != nil {
		return UnitClaim{}, err
	}
	if err = lockRankedUnitTx(ctx, tx, team, unit); err != nil {
		return UnitClaim{}, err
	}
	if err = lockClaimOwnerTx(ctx, tx, team, unit); err != nil {
		return UnitClaim{}, err
	}
	if err = lockStateRunTx(ctx, tx, team, unit.RunID); err != nil {
		return UnitClaim{}, err
	}
	if err = checkClaimableUnitTx(ctx, tx, team, unit); err != nil {
		return UnitClaim{}, err
	}
	placement, err := unitPlacementTx(ctx, tx, team, unit, demand.CapabilityPrefix, profile, requirements)
	if err != nil {
		return UnitClaim{}, err
	}
	if placement.hold {
		return UnitClaim{}, errRankingHold
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM unit_asks WHERE team=? AND run_id=? AND unit_kind=? AND node_id=?`, team, unit.RunID, unit.Kind, unit.NodeID); err != nil {
		return UnitClaim{}, err
	}
	unitID, err := encodeAwardUnit(unit)
	if err != nil {
		return UnitClaim{}, err
	}
	spec := capability.AwardSpec{Claimant: p.Authority, Unit: unit.Kind, UnitID: unitID, Purpose: claimPurpose(unit.Kind), Delivery: capability.DeliveryDirect}
	if launcherClaimant(p.Capability) {
		spec.Delivery = capability.DeliveryBootstrap
	}
	row, err := tx.operator.loadAwardRowTx(ctx, tx, spec)
	if err != nil {
		return UnitClaim{}, err
	}
	now := tx.now()
	var deadline time.Time
	if row.expiry.Valid {
		deadline = time.Unix(0, row.expiry.Int64).UTC()
	}
	until, err := leaseBound(now, deadline, deref(p.Capability.ExpiresAt))
	if err != nil {
		return UnitClaim{}, err
	}
	row.holder, row.generation, row.lease = p.Capability.Subject.ID, row.generation+1, sql.NullInt64{Int64: until.UnixNano(), Valid: true}
	issued, err := tx.store.issueTx(ctx, tx, capability.IssueRequest{Kind: capability.Award, Award: &spec}, &row)
	if err != nil {
		return UnitClaim{}, err
	}
	funded, err := fundedBoundTx(ctx, tx, team, unit, row.generation, now)
	if err != nil {
		return UnitClaim{}, err
	}
	if until, err = leaseBoundTx(ctx, tx, issued.Capability.Prefix, now, until, funded); err != nil {
		return UnitClaim{}, err
	}
	claim := UnitClaim{Team: team, Kind: unit.Kind, RunID: unit.RunID, NodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Award: ClaimAward{CapabilityPrefix: issued.Capability.Prefix, Holder: row.holder, Generation: row.generation, LeaseExpiresAt: until}, Bearer: []byte(issued.Raw)}
	if unit.Kind == capability.UnitNode {
		actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Holder: row.holder, Generation: row.generation}
		_, err = transitionNodeTx(ctx, tx, team, NodeTransition{Ref: controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, To: NodeClaimed, Cause: CauseAward, Actor: actor, Extra: TransitionExtra{Claim: &claim.Award}}, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE nodes SET placement_reason=? WHERE team=? AND run_id=? AND node_id=?`, placement.reason, team, unit.RunID, unit.NodeID)
		}
	} else {
		err = writeOwnerClaimTx(ctx, tx, claim)
	}
	if err != nil {
		return UnitClaim{}, err
	}
	deadline = until
	if unit.Kind == capability.UnitEval {
		var ownerUntil int64
		if err := tx.QueryRowContext(ctx, `SELECT until FROM leases WHERE name='controller.owner'`).Scan(&ownerUntil); err != nil {
			return UnitClaim{}, err
		}
		if ownerUntil < deadline.UnixNano() {
			deadline = time.Unix(0, ownerUntil).UTC()
		}
	}
	if !deadline.After(tx.now()) {
		return UnitClaim{}, holderRefusal()
	}
	if unit.Kind == capability.UnitTrigger || unit.Kind == capability.UnitPlan {
		claim.Owner = OwnerLeaseRef{Kind: OwnerTrigger, RunID: unit.RunID, Holder: row.holder, Generation: row.generation}
	}
	if unit.Kind == capability.UnitEval {
		claim.Owner = OwnerLeaseRef{Kind: OwnerEval, RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Holder: row.holder, Generation: row.generation}
	}
	if p.Capability.Purpose == capability.PurposeHere && unit.Kind == capability.UnitTrigger {
		observation := demand
		observation.CapabilityPrefix = claim.Award.CapabilityPrefix
		observation.Labels = match.SelfAsserted(observation.Labels)
		body, err := json.Marshal(runnerPoll{UnitDemand: observation, ObservedAt: tx.now()})
		if err != nil {
			return UnitClaim{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET claim_observation_json=?,last_used_at=? WHERE team=? AND prefix=?`, body, tx.now().UnixNano(), team, observation.CapabilityPrefix); err != nil {
			return UnitClaim{}, err
		}
	}
	return claim, nil
}

func validateClaimUnit(unit UnitRef) error {
	if unit.RunID == "" {
		return authorityError(apierr.BadRequest, "claim requires a run")
	}
	switch unit.Kind {
	case capability.UnitTrigger, capability.UnitPlan:
		if unit.NodeID != "" || unit.SourceGeneration != 0 {
			return authorityError(apierr.BadRequest, "owner claim has a job identity")
		}
	case capability.UnitNode:
		if unit.NodeID == "" || unit.SourceGeneration != 0 {
			return authorityError(apierr.BadRequest, "invalid job claim identity")
		}
	case capability.UnitEval:
		if unit.NodeID == "" || unit.SourceGeneration < 0 {
			return authorityError(apierr.BadRequest, "invalid evaluation claim identity")
		}
	default:
		return authorityError(apierr.BadRequest, "unknown claim unit")
	}
	return nil
}

func claimPurpose(kind capability.UnitKind) capability.AwardPurpose {
	switch kind {
	case capability.UnitTrigger:
		return capability.PurposeTrigger
	case capability.UnitPlan:
		return capability.PurposePlan
	case capability.UnitEval:
		return capability.PurposeEval
	default:
		return capability.PurposeWork
	}
}
