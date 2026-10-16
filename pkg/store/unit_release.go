package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Store) releaseUnit(ctx context.Context, claim UnitClaim, prefix string) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		var holder string
		if err := tx.QueryRowContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner'`+tx.forKeyShare()).Scan(&holder); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		principal, err := tx.ResolveAuthority(ctx, ref)
		if err != nil {
			return err
		}
		c := principal.Capability
		if c.Audience != capability.Controller || c.Team != string(claim.Team) || c.Unit != claim.Kind || c.Prefix != claim.Award.CapabilityPrefix || c.Binding.Holder != claim.Award.Holder {
			return holderRefusal()
		}
		unit, err := decodeAwardUnit(c.Unit, c.UnitID)
		if err != nil {
			return err
		}
		if unit.RunID != claim.RunID || unit.NodeID != claim.NodeID || unit.SourceGeneration != claim.SourceGeneration {
			return holderRefusal()
		}
		generation := c.Binding.Generation
		if c.Binding.Expansion != nil {
			generation = c.Binding.Expansion.Generation
		}
		if generation != claim.Award.Generation {
			return holderRefusal()
		}
		if claim.Kind == capability.UnitNode {
			if err := lockStateRunTx(ctx, tx, claim.Team, claim.RunID); err != nil {
				return err
			}
		}
		row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
		if err != nil {
			return err
		}
		now := tx.now()
		if row.team != string(claim.Team) || row.prefix != prefix || row.holder != claim.Award.Holder || row.generation != generation || !row.lease.Valid || row.lease.Int64 <= now.UnixNano() {
			return holderRefusal()
		}
		if claim.Kind == capability.UnitNode {
			node, err := readNodeTx(ctx, tx, claim.Team, NodeRef{claim.RunID, claim.NodeID})
			if err != nil {
				return err
			}
			state, err := nodeState(node)
			if err != nil {
				return err
			}
			if state != NodeClaimed || node.CancelRequestedAt != nil {
				return holderRefusal()
			}
			actor := Actor{Kind: ActorHolder, Authority: ref, CapabilityPrefix: prefix, Holder: claim.Award.Holder, Generation: generation}
			_, err = transitionNodeTx(ctx, tx, claim.Team, NodeTransition{Ref: node.Ref, To: NodePending, Cause: CauseRelease, Actor: actor, SourceAttempt: &node.Attempt}, now)
			return err
		}
		if err := lockStateRunTx(ctx, tx, claim.Team, claim.RunID); err != nil {
			return err
		}
		return releaseOwnerUnitTx(ctx, tx, claim, unit)
	})
}

func releaseOwnerUnitTx(ctx context.Context, tx *storeTx, claim UnitClaim, unit UnitRef) error {
	var status string
	var started sql.NullInt64
	var disruptions int
	var err error
	if claim.Kind == capability.UnitEval {
		err = tx.QueryRowContext(ctx, `SELECT status,started_at,disruptions FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, claim.Team, claim.RunID, claim.NodeID, claim.SourceGeneration).Scan(&status, &started, &disruptions)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT status,started_at,disruptions FROM triggers WHERE team=? AND id=?`, claim.Team, claim.RunID).Scan(&status, &started, &disruptions)
	}
	if err != nil {
		return err
	}
	if status != "claimed" || started.Valid {
		return holderRefusal()
	}
	target := expiredUnit{team: claim.Team, ref: NodeRef{claim.RunID, claim.NodeID}, kind: claim.Kind, sourceGeneration: unit.SourceGeneration}
	owner := ownerRecovery{generation: claim.Award.Generation, disruptions: disruptions}
	if claim.Kind == capability.UnitEval {
		return writeEvalRecoveryTx(ctx, tx, target, owner, "pending", tx.now())
	}
	return writeOwnerRecoveryTx(ctx, tx, target, owner, "pending", Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}, tx.now())
}
