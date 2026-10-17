package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (s *Store) claimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	var out UnitClaim
	err := s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		if err := lockClaimantTx(ctx, tx, demand.CapabilityPrefix); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
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
		if demand.Target != nil {
			out, err = claimUnitTx(ctx, tx, demand, *demand.Target)
			return err
		}
		unit, err := queuedUnitTx(ctx, tx, p.Capability, demand, kinds)
		if err != nil {
			return err
		}
		out, err = claimUnitTx(ctx, tx, demand, unit)
		return err
	})
	if err != nil {
		return UnitClaim{}, err
	}
	return out, nil
}

// safety: issuing the award stamps the claimant's last_used_at while the run is
// locked, and authority resolution holds the claimant's row in share mode. Taking
// the stronger lock first keeps two claims by one claimant from each holding a
// share lock the other's update waits on while both queue for the run.
func lockClaimantTx(ctx context.Context, tx *storeTx, prefix string) error {
	var held string
	err := tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE prefix=?`+tx.forNoKeyUpdate(), prefix).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

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
		if kind == capability.UnitTrigger {
			grant = capability.TriggersClaim
		}
		allowed := slices.Contains(c.Grants, grant) && (kind == capability.UnitNode || kind == capability.UnitTrigger)
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

func claimUnitTx(ctx context.Context, tx *storeTx, demand UnitDemand, unit UnitRef) (UnitClaim, error) {
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
	profile, requirements, err := matchingInputsTx(ctx, tx, demand, unit)
	if err != nil {
		return UnitClaim{}, err
	}
	if verdict := match.Evaluate(profile, requirements); !verdict.OK() {
		return UnitClaim{}, authorityError(apierr.Forbidden, "work does not match claimant: "+verdict.String())
	}
	if verdict := match.Fits(profile.Capacity, demand.Available, requirements.Request); !verdict.OK() {
		return UnitClaim{}, authorityError(apierr.LockHeld, "claimant capacity is unavailable")
	}
	if err = checkClaimantBindingTx(ctx, tx, p.Capability, unit); err != nil {
		return UnitClaim{}, err
	}
	var team Team
	if err = tx.QueryRowContext(ctx, `SELECT team FROM runs WHERE id=?`, unit.RunID).Scan(&team); err != nil {
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
	placement, err := unitPlacementTx(ctx, tx, team, unit, profile, requirements)
	if err != nil {
		return UnitClaim{}, err
	}
	if placement.hold {
		return UnitClaim{}, authorityError(apierr.LockHeld, "work is waiting for a preferred runner")
	}
	unitID, err := encodeAwardUnit(unit)
	if err != nil {
		return UnitClaim{}, err
	}
	spec := capability.AwardSpec{Claimant: p.Authority, Unit: unit.Kind, UnitID: unitID, Purpose: claimPurpose(unit.Kind), Delivery: capability.DeliveryDirect}
	if launcherClaimant(p.Capability) {
		spec.Delivery = capability.DeliveryBootstrap
	}
	row, err := loadAwardRowTx(ctx, tx, spec)
	if err != nil {
		return UnitClaim{}, err
	}
	now := tx.now()
	until := now.Add(limits.DefaultLease)
	if row.expiry.Valid && time.Unix(0, row.expiry.Int64).Before(until) {
		until = time.Unix(0, row.expiry.Int64).UTC()
	}
	if p.Capability.ExpiresAt != nil && p.Capability.ExpiresAt.Before(until) {
		until = *p.Capability.ExpiresAt
	}
	if !until.After(now) {
		return UnitClaim{}, holderRefusal()
	}
	row.holder, row.generation, row.lease = p.Capability.Subject.ID, row.generation+1, sql.NullInt64{Int64: until.UnixNano(), Valid: true}
	issued, err := tx.store.issueTx(ctx, tx, capability.IssueRequest{Kind: capability.Award, Award: &spec}, &row)
	if err != nil {
		return UnitClaim{}, err
	}
	if issued.Capability.ExpiresAt != nil && issued.Capability.ExpiresAt.Before(until) {
		until = *issued.Capability.ExpiresAt
	}
	claim := UnitClaim{Team: team, Kind: unit.Kind, RunID: unit.RunID, NodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Award: ClaimAward{CapabilityPrefix: issued.Capability.Prefix, Holder: row.holder, Generation: row.generation, LeaseExpiresAt: until}, Bearer: []byte(issued.Raw)}
	if unit.Kind == capability.UnitNode {
		actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Holder: row.holder, Generation: row.generation}
		_, err = transitionNodeTx(ctx, tx, team, NodeTransition{Ref: NodeRef{unit.RunID, unit.NodeID}, To: NodeClaimed, Cause: CauseAward, Actor: actor, Extra: TransitionExtra{Claim: &claim.Award}}, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE nodes SET placement_reason=? WHERE team=? AND run_id=? AND node_id=?`, placement.reason, team, unit.RunID, unit.NodeID)
		}
	} else {
		err = writeOwnerClaimTx(ctx, tx, claim)
	}
	if err != nil {
		return UnitClaim{}, err
	}
	deadline := until
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
