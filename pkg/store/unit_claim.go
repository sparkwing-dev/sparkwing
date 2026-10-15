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
		p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
		if err != nil {
			return err
		}
		kinds, err := claimKinds(p.Capability, demand)
		if err != nil {
			return err
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

func lockClaimControllerTx(ctx context.Context, tx *storeTx) error {
	var holder string
	err := tx.QueryRowContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner'`+tx.forUpdate()).Scan(&holder)
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
	if demand.Target != nil {
		if len(kinds) > 0 && !slices.Contains(kinds, demand.Target.Kind) {
			return nil, authorityError(apierr.BadRequest, "claim target is outside requested kinds")
		}
		kinds = []capability.UnitKind{demand.Target.Kind}
	}
	var allowed []capability.UnitKind
	for _, policy := range capability.AwardPolicies {
		if policy.Purpose == capability.PurposeHere {
			continue
		}
		for _, g := range policy.ClaimGrants {
			if slices.Contains(c.Grants, g) {
				allowed = append(allowed, policy.Unit)
				break
			}
		}
	}
	if len(kinds) == 0 {
		kinds = allowed
	}
	for _, kind := range kinds {
		if !slices.Contains(allowed, kind) {
			return nil, authorityError(apierr.Forbidden, "claimant lacks the unit claim grant")
		}
	}
	if len(kinds) == 0 {
		return nil, authorityError(apierr.Forbidden, "claimant lacks a claim grant")
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
	} else {
		err = writeOwnerClaimTx(ctx, tx, claim)
	}
	if err != nil {
		return UnitClaim{}, err
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
		if unit.NodeID == "" || unit.SourceGeneration <= 0 {
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
