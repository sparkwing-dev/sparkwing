package store

import (
	"context"
	"database/sql"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (s *Store) releaseUnit(ctx context.Context, claim UnitClaim, prefix string) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		principal, err := resolveTeamAuthorityTx(ctx, tx, claim.Team, ref, sameTeam)
		if err != nil {
			return err
		}
		c := principal.Capability
		if c.Audience != capability.Controller || c.Unit != claim.Kind || c.Prefix != claim.Award.CapabilityPrefix || c.Binding.Holder != claim.Award.Holder {
			return holderRefusal()
		}
		unit, err := decodeAwardUnit(c.Unit, c.UnitID)
		if err != nil {
			return err
		}
		if string(unit.RunID) != claim.RunID || string(unit.NodeID) != claim.NodeID || unit.SourceGeneration != claim.SourceGeneration {
			return holderRefusal()
		}
		generation := c.Binding.Generation
		if c.Binding.Expansion != nil {
			generation = c.Binding.Expansion.Generation
		}
		if generation != int64(claim.Award.Generation) {
			return holderRefusal()
		}
		row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
		if err != nil {
			return err
		}
		now := tx.now(ctx)
		if row.team != string(claim.Team) || row.prefix != prefix || row.holder != claim.Award.Holder || row.generation != generation || !row.lease.Valid || row.lease.Int64 <= now.UnixNano() {
			return holderRefusal()
		}
		if claim.Kind == capability.UnitNode {
			node, err := readNodeTx(ctx, tx, claim.Team, runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)})
			if err != nil {
				return err
			}
			state, err := nodeState(node)
			if err != nil {
				return err
			}
			if state != runstate.NodeClaimed || node.CancelRequestedAt != nil {
				return holderRefusal()
			}
			actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: ref, CapabilityPrefix: prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: runspec.ClaimGeneration(generation)}}
			_, err = transitionNodeTx(ctx, tx, claim.Team, nodeTransition{Ref: node.Ref, To: nodePending, Cause: runstate.CauseRelease, Actor: actor, SourceAttempt: &node.Attempt})
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
	// A returned reservation has no writer; signed slots still require a truthful final seal.
	if _, err := tx.execContext(ctx, `DELETE FROM log_streams WHERE team=? AND run_id=? AND subject_kind='owner_unit'
		AND unit_kind=? AND generation=? AND COALESCE(source_node_id,'')=? AND COALESCE(source_generation,0)=?
		AND uploaded_through=0 AND sealed_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM log_slots l WHERE l.team=log_streams.team AND l.run_id=log_streams.run_id AND l.stream_id=log_streams.id)`,
		claim.Team, claim.RunID, claim.Kind, claim.Award.Generation, claim.NodeID, claim.SourceGeneration); err != nil {
		return err
	}
	target := expiredUnit{team: claim.Team, ref: runspec.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}, kind: claim.Kind, sourceGeneration: unit.SourceGeneration}
	owner := ownerRecovery{generation: int64(claim.Award.Generation), disruptions: disruptions}
	if claim.Kind == capability.UnitEval {
		return writeEvalRecoveryTx(ctx, tx, target, owner, "pending", tx.now(ctx))
	}
	return writeOwnerRecoveryTx(ctx, tx, target, owner, "pending", runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}, tx.now(ctx))
}
