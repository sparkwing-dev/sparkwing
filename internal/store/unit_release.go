package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
		now, clockErr1 := tx.now(ctx)
		if clockErr1 != nil {
			return clockErr1
		}
		if row.team != string(claim.Team) || row.prefix != prefix || row.holder != claim.Award.Holder || row.generation != generation || !row.lease.Valid || row.lease.Int64 <= now.UnixNano() {
			return holderRefusal()
		}
		if claim.Kind == capability.UnitNode {
			node, err := readNodeTx(ctx, tx, claim.Team, controllerwire.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)})
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
			actor := Actor{Kind: ActorHolder, Authority: ref, CapabilityPrefix: prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: runspec.ClaimGeneration(generation)}}
			_, err = transitionNodeTx(ctx, tx, claim.Team, nodeTransition{Ref: node.Ref, To: nodePending, Cause: CauseRelease, Actor: actor, SourceAttempt: &node.Attempt})
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
	target := expiredUnit{team: claim.Team, ref: controllerwire.NodeRef{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID)}, kind: claim.Kind, sourceGeneration: unit.SourceGeneration}
	owner := ownerRecovery{generation: int64(claim.Award.Generation), disruptions: disruptions}
	if claim.Kind == capability.UnitEval {
		clockTime2, clockErr2 := tx.now(ctx)
		if clockErr2 != nil {
			return clockErr2
		}
		return writeEvalRecoveryTx(ctx, tx, target, owner, "pending", clockTime2)
	}
	clockTime3, clockErr3 := tx.now(ctx)
	if clockErr3 != nil {
		return clockErr3
	}
	return writeOwnerRecoveryTx(ctx, tx, target, owner, "pending", Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: runspec.NodeID(claim.NodeID), Holder: claim.Award.Holder, Generation: claim.Award.Generation}}, clockTime3)
}
