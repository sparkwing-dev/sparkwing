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
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func (o *Operator) claimUnit(ctx context.Context, demand UnitDemand) (UnitClaim, error) {
	var out UnitClaim
	var waiting error
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		out, waiting = UnitClaim{}, nil
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		claimant := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{claimant}); err != nil {
			return err
		}
		p, err := tx.ResolveAuthority(ctx, claimant)
		if err != nil {
			return err
		}
		if demand.Target != nil {
			if _, err := tx.operator.claimTargetTeamTx(ctx, tx, p.Capability, *demand.Target); err != nil {
				return err
			}
		}
		kinds, err := claimKinds(p.Capability, demand)
		if err != nil {
			return err
		}
		if demand.FreeSlots != nil && *demand.FreeSlots <= 0 {
			waiting = notFound("eligible work", "")
			return nil
		}
		unit, err := queuedUnitTx(ctx, tx, demand, kinds)
		if errors.Is(err, ErrNotFound) {
			waiting = err
			if demand.Target != nil {
				waiting = tx.operator.claimRefusalTx(ctx, tx, demand, *demand.Target)
			}
			return nil
		}
		if err != nil {
			return err
		}
		out, err = tx.operator.claimUnitTx(ctx, tx, demand, unit)
		if err == errRankingHold {
			waiting, err = err, nil
			if demand.Target == nil {
				waiting = notFound("eligible work", "")
			}
		}

		return err
	})
	if err == nil {
		err = waiting
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
			allowed = kind == capability.UnitTrigger && demand.Target != nil && string(demand.Target.RunID) == c.Binding.RunID && slices.Contains(c.Grants, capability.TriggerHost)
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
			unpriced.RunID, unpriced.NodeID = string(unit.RunID), string(unit.NodeID)
		}
	}()

	if err := validateClaimUnit(unit); err != nil {
		return UnitClaim{}, err
	}
	if err := lockClaimQueueTx(ctx, tx); err != nil {
		return UnitClaim{}, err
	}
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil {
		return UnitClaim{}, err
	}
	team, err := tx.operator.claimTargetTeamTx(ctx, tx, p.Capability, unit)
	if err != nil {
		return UnitClaim{}, err
	}
	if _, err = claimKinds(p.Capability, UnitDemand{Target: &unit}); err != nil {
		return UnitClaim{}, err
	}
	named := demand
	named.Kinds = nil
	named.Target = &unit
	if _, err := queuedUnitTx(ctx, tx, named, []capability.UnitKind{unit.Kind}); err != nil {
		return UnitClaim{}, err
	}
	profile, requirements, err := readMatchingInputsTx(ctx, tx, named, unit, true)
	if err != nil {
		return UnitClaim{}, err
	}
	if err = checkClaimantBindingTx(ctx, tx, p.Capability, unit); err != nil {
		return UnitClaim{}, err
	}
	if err = lockClaimOwnerTx(ctx, tx, team, unit); err != nil {
		return UnitClaim{}, err
	}
	if err = lockStateRunTx(ctx, tx, team, string(unit.RunID)); err != nil {
		return UnitClaim{}, err
	}
	if err = checkClaimableUnitTx(ctx, tx, team, unit); err != nil {
		return UnitClaim{}, err
	}
	policy, _ := claimPlacementFromContext(ctx)
	placement := placementNone
	if unit.Kind == capability.UnitNode {
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT execution_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&body); err != nil {
			return UnitClaim{}, err
		}
		var execution runspec.JobExecutionSettings
		if err := json.Unmarshal(body, &execution); err != nil {
			return UnitClaim{}, err
		}
		preferences := policy.preferencesFor(execution.Prefers.Value)
		if len(preferences) > 0 && len(preferences[0].Prefers) > 0 {
			placement = placementFallback
			if placementRank(profile, requirements, preferences) == 0 {
				placement = placementPreferred
			}
		}
	}
	if err := consumeClaimSlotTx(ctx, tx, demand.CapabilityPrefix, p.Capability.Team); err != nil {
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
	now := tx.now(ctx)
	var deadline time.Time
	if row.expiry.Valid {
		deadline = time.Unix(0, row.expiry.Int64).UTC()
	}
	until, err := leaseBound(now, deadline, deref(p.Capability.ExpiresAt))
	if err != nil {
		return UnitClaim{}, err
	}
	chain, err := authorityChainTx(ctx, tx, p.Authority, false)
	if err != nil {
		return UnitClaim{}, err
	}
	if meteredPlacement(p.Capability, chain[len(chain)-1].principal.Capability) {
		selected, err := unitBillingPriceTx(ctx, tx, requirements.Selector, profile)
		if err != nil {
			return UnitClaim{}, err
		}
		row.selectedPrice = &selected
	}
	row.holder, row.generation, row.lease = p.Capability.Subject.ID, row.generation+1, sql.NullInt64{Int64: until.UnixNano(), Valid: true}
	issued, err := tx.store.issueTx(ctx, tx, capability.IssueRequest{Kind: capability.Award, Award: &spec}, &row)
	if err != nil {
		return UnitClaim{}, err
	}
	// Issuance can start a billable interval after the initial lease check.
	now = tx.now(ctx)
	funded, err := fundedBoundTx(ctx, tx, team, unit, row.generation, now)
	if err != nil {
		return UnitClaim{}, err
	}
	if until, err = leaseBoundTx(ctx, tx, issued.Capability.Prefix, now, until, funded); err != nil {
		return UnitClaim{}, err
	}
	claim := UnitClaim{Team: team, Kind: unit.Kind, RunID: string(unit.RunID), NodeID: string(unit.NodeID), SourceGeneration: unit.SourceGeneration, Award: runstate.ClaimAward{CapabilityPrefix: issued.Capability.Prefix, Holder: row.holder, Generation: runspec.ClaimGeneration(row.generation), LeaseExpiresAt: until}, Bearer: []byte(issued.Raw)}
	if unit.Kind == capability.UnitNode {
		actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, NodeFence: runspec.NodeFence{RunID: unit.RunID, NodeID: unit.NodeID, Holder: row.holder, Generation: runspec.ClaimGeneration(row.generation)}}
		_, err = transitionNodeTx(ctx, tx, team, nodeTransition{Ref: runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID}, To: runstate.NodeClaimed, Cause: runstate.CauseAward, Actor: actor, Extra: transitionExtra{Claim: &claim.Award}})
		if err == nil {
			_, err = tx.execContext(ctx, `UPDATE nodes SET placement_reason=? WHERE team=? AND run_id=? AND node_id=?`, placement, team, unit.RunID, unit.NodeID)
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
	current := tx.now(ctx)
	if !deadline.After(current) {
		return UnitClaim{}, holderRefusal()
	}
	if unit.Kind == capability.UnitTrigger || unit.Kind == capability.UnitPlan {
		claim.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: unit.RunID, Holder: row.holder, Generation: runspec.ClaimGeneration(row.generation)}}
	}
	if unit.Kind == capability.UnitEval {
		claim.Owner = runstate.OwnerLeaseRef{Kind: runstate.OwnerEval, EvalFence: runspec.EvalFence{RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: runspec.ClaimGeneration(unit.SourceGeneration), Holder: row.holder, Generation: runspec.ClaimGeneration(row.generation)}}
	}
	if p.Capability.Purpose == capability.PurposeHere && unit.Kind == capability.UnitTrigger {
		observation := demand
		observation.CapabilityPrefix = claim.Award.CapabilityPrefix
		observation.Target, observation.Kinds = nil, nil
		observation.Labels = match.SelfAsserted(observation.Labels)
		body, err := json.Marshal(runnerPoll{UnitDemand: observation, ObservedAt: tx.now(ctx)})
		if err != nil {
			return UnitClaim{}, err
		}
		if _, err := tx.execContext(ctx, `UPDATE capabilities SET claim_observation_json=?,last_used_at=? WHERE team=? AND prefix=?`, body, tx.now(ctx).UnixNano(), team, observation.CapabilityPrefix); err != nil {
			return UnitClaim{}, err
		}
	}
	if err := recordClaimAwardTx(ctx, tx, team); err != nil {
		return UnitClaim{}, err
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

func (o *Operator) claimRefusalTx(ctx context.Context, tx *storeTx, demand UnitDemand, unit UnitRef) error {
	p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix})
	if err != nil {
		return err
	}
	team, err := tx.operator.claimTargetTeamTx(ctx, tx, p.Capability, unit)
	if err != nil {
		return err
	}
	diagnostic, err := explainUnitPlacementTx(ctx, tx, team, unit, demand)
	if err != nil {
		return err
	}
	if !diagnostic.ok() {
		return diagnostic.refused
	}
	if err := checkClaimableUnitTx(ctx, tx, team, unit); err != nil {
		return err
	}
	diagnostic, err = explainUnitFundingTx(ctx, tx, team, unit, diagnostic)
	if err != nil {
		return err
	}
	if !diagnostic.ok() {
		return diagnostic.refused
	}
	return errRankingHold
}

func (o *Operator) claimTargetTeamTx(ctx context.Context, tx *storeTx, claimant capability.Capability, unit UnitRef) (Team, error) {
	query, args := `SELECT team FROM runs WHERE id=? AND team=?`, []any{unit.RunID, claimant.Team}
	if capability.CloudLauncher(claimant) {
		query, args = `SELECT team FROM runs WHERE id=?`, []any{unit.RunID}
	}
	var team Team
	err := tx.QueryRowContext(ctx, query, args...).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("run", string(unit.RunID))
	}
	return team, err
}
