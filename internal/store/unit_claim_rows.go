package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func checkClaimantBindingTx(ctx context.Context, tx *storeTx, c capability.Capability, unit UnitRef) error {
	if c.Binding.Kind == capability.BindingNone || c.Binding.Kind == capability.BindingPush {
		return nil
	}
	if c.Binding.Kind != capability.BindingTrigger || c.Binding.RunID != string(unit.RunID) {
		return holderRefusal()
	}
	if c.Purpose == capability.PurposeHere {
		if unit.Kind != capability.UnitTrigger {
			return holderRefusal()
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM triggers WHERE team=? AND id=?`, c.Team, unit.RunID).Scan(&generation); err != nil {
			return err
		}
		if generation == 0 && (c.Binding.ClaimBy == nil || !c.Binding.ClaimBy.After(tx.now(ctx))) {
			return holderRefusal()
		}
		return nil
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: unit.RunID, Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}}

	return checkOwnerTx(ctx, tx, Team(c.Team), actor, string(unit.RunID))
}

func checkClaimableUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	if err := lockOwnerUnitTx(ctx, tx, team, unit); err != nil {
		return err
	}
	now := tx.now(ctx).UnixNano()
	var expired int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triggers WHERE team=? AND id=? AND expires_at<=?`, team, unit.RunID, now).Scan(&expired); err != nil {
		return err
	}
	if expired > 0 {
		return holderRefusal()
	}
	run, err := readRunTx(ctx, tx, team, string(unit.RunID))
	if err != nil {
		return err
	}
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode == "publish" {
		return holderRefusal()
	}
	wait := runClaimWait(run)
	if unit.Kind == capability.UnitNode {
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
		if err != nil {
			return err
		}
		state, err := nodeState(row)
		if err != nil {
			return err
		}
		if state != runstate.NodeReady || row.CancelRequestedAt != nil || row.ReadyAt.UnixNano() > now {
			return holderRefusal()
		}
		if wait.Set && wait.Value > 0 && (row.QueueStartedAt == nil || !row.QueueStartedAt.Add(wait.Value).After(time.Unix(0, now))) {
			return holderRefusal()
		}
		return nil
	}
	var status, holder, prefix string
	var expiry sql.NullInt64
	if unit.Kind == capability.UnitEval {
		err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,expires_at FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, team, unit.RunID, unit.NodeID, unit.SourceGeneration).Scan(&status, &holder, &prefix, &expiry)
		if err != nil {
			return err
		}
		if status != "pending" || holder != "" || prefix != "" {
			return holderRefusal()
		}
		if err := evaluationSourceTx(ctx, tx, team, unit); err != nil {
			return err
		}
	} else {
		var available int64
		var cancelled, started sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,expires_at,available_at,cancel_requested_at,started_at FROM triggers WHERE team=? AND id=? AND unit_kind=?`, team, unit.RunID, unit.Kind).Scan(&status, &holder, &prefix, &expiry, &available, &cancelled, &started)
		if err != nil {
			return err
		}
		if available > now || cancelled.Valid {
			return holderRefusal()
		}
		if !started.Valid && wait.Set && wait.Value > 0 && !run.CreatedAt.Add(wait.Value).After(time.Unix(0, now)) {
			return holderRefusal()
		}
	}
	if status != "pending" || holder != "" || prefix != "" || (expiry.Valid && expiry.Int64 <= now) {
		return holderRefusal()
	}
	return nil
}

func writeOwnerClaimTx(ctx context.Context, tx *storeTx, claim UnitClaim) error {
	var result sql.Result
	var err error
	if claim.Kind == capability.UnitEval {
		result, err = tx.execContext(ctx, `UPDATE eval_units SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,claimed_at=?,lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND status='pending' AND generation=? AND claimed_by IS NULL AND capability_prefix=''`, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.Generation, tx.now(ctx).UnixNano(), claim.Award.LeaseExpiresAt.UnixNano(), claim.Team, claim.RunID, claim.NodeID, claim.SourceGeneration, claim.Award.Generation-1)
	} else {
		result, err = tx.execContext(ctx, `UPDATE triggers SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,claimed_at=?,lease_expires_at=? WHERE team=? AND id=? AND unit_kind=? AND status='pending' AND generation=? AND claimed_by IS NULL AND capability_prefix=''`, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.Generation, tx.now(ctx).UnixNano(), claim.Award.LeaseExpiresAt.UnixNano(), claim.Team, claim.RunID, claim.Kind, claim.Award.Generation-1)
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return holderRefusal()
	}
	return nil
}
