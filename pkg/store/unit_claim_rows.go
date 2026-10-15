package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func lockClaimOwnerTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	var id string
	switch unit.Kind {
	case capability.UnitTrigger, capability.UnitPlan:
		return tx.QueryRowContext(ctx, `SELECT id FROM triggers WHERE team=? AND id=? AND unit_kind=?`+tx.forUpdate(), team, unit.RunID, unit.Kind).Scan(&id)
	case capability.UnitEval:
		return tx.QueryRowContext(ctx, `SELECT run_id FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`+tx.forUpdate(), team, unit.RunID, unit.NodeID, unit.SourceGeneration).Scan(&id)
	default:
		return nil
	}
}

func checkClaimantBindingTx(ctx context.Context, tx *storeTx, c capability.Capability, unit UnitRef) error {
	if c.Binding.Kind == capability.BindingNone {
		return nil
	}
	if c.Binding.Kind != capability.BindingTrigger || c.Binding.RunID != unit.RunID {
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
		if generation == 0 && (c.Binding.ClaimBy == nil || !c.Binding.ClaimBy.After(tx.now())) {
			return holderRefusal()
		}
		return nil
	}
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix}, CapabilityPrefix: c.Prefix, Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: unit.RunID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}}
	return checkOwnerTx(ctx, tx, Team(c.Team), actor, unit.RunID, tx.now())
}

func checkClaimableUnitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) error {
	now := tx.now().UnixNano()
	run, err := readRunTx(ctx, tx, team, unit.RunID)
	if err != nil {
		return err
	}
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode == "publish" {
		return holderRefusal()
	}
	wait := runClaimWait(run)
	if unit.Kind == capability.UnitNode {
		row, err := readNodeTx(ctx, tx, team, NodeRef{unit.RunID, unit.NodeID})
		if err != nil {
			return err
		}
		state, err := nodeState(row)
		if err != nil {
			return err
		}
		if state != NodeReady || row.CancelRequestedAt != nil || row.ReadyAt.UnixNano() > now {
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
		var ownerName string
		var ownerGeneration int64
		err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,expires_at,owner_lease_name,owner_generation FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`+tx.forUpdate(), team, unit.RunID, unit.NodeID, unit.SourceGeneration).Scan(&status, &holder, &prefix, &expiry, &ownerName, &ownerGeneration)
		if err != nil {
			return err
		}
		var actualGeneration, until int64
		if err = tx.QueryRowContext(ctx, `SELECT generation,until FROM leases WHERE name=?`, ownerName).Scan(&actualGeneration, &until); err != nil {
			return err
		}
		if ownerName != "controller.owner" || until <= now || status != "pending" || holder != "" || prefix != "" {
			return holderRefusal()
		}
		if actualGeneration != ownerGeneration {
			if _, err = tx.ExecContext(ctx, `UPDATE eval_units SET owner_generation=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, actualGeneration, team, unit.RunID, unit.NodeID, unit.SourceGeneration); err != nil {
				return err
			}
		}
		var generation int64
		var outcome, expansion string
		if err = tx.QueryRowContext(ctx, `SELECT generation,outcome,expansion FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&generation, &outcome, &expansion); err != nil {
			return err
		}
		if generation != unit.SourceGeneration || (outcome != "success" && outcome != "memoized") || expansion != "expected" {
			return holderRefusal()
		}
	} else {
		var available int64
		var cancelled, started sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,expires_at,available_at,cancel_requested_at,started_at FROM triggers WHERE team=? AND id=? AND unit_kind=?`+tx.forUpdate(), team, unit.RunID, unit.Kind).Scan(&status, &holder, &prefix, &expiry, &available, &cancelled, &started)
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
		result, err = tx.ExecContext(ctx, `UPDATE eval_units SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,claimed_at=?,lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND status='pending' AND generation=? AND claimed_by IS NULL AND capability_prefix=''`, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.Generation, tx.now().UnixNano(), claim.Award.LeaseExpiresAt.UnixNano(), claim.Team, claim.RunID, claim.NodeID, claim.SourceGeneration, claim.Award.Generation-1)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE triggers SET status='claimed',claimed_by=?,capability_prefix=?,generation=?,claimed_at=?,lease_expires_at=? WHERE team=? AND id=? AND unit_kind=? AND status='pending' AND generation=? AND claimed_by IS NULL AND capability_prefix=''`, claim.Award.Holder, claim.Award.CapabilityPrefix, claim.Award.Generation, tx.now().UnixNano(), claim.Award.LeaseExpiresAt.UnixNano(), claim.Team, claim.RunID, claim.Kind, claim.Award.Generation-1)
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

type queuedClaim struct {
	unit    UnitRef
	created int64
	score   int
}

func queuedUnitTx(ctx context.Context, tx *storeTx, c capability.Capability, demand UnitDemand, kinds []capability.UnitKind) (UnitRef, error) {
	var candidates []queuedClaim
	for _, kind := range kinds {
		query, args := queuedClaimQuery(c, kind, tx.now().UnixNano())
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return UnitRef{}, err
		}
		for rows.Next() {
			v := queuedClaim{unit: UnitRef{Kind: kind}}
			if err = rows.Scan(&v.unit.RunID, &v.unit.NodeID, &v.unit.SourceGeneration, &v.created); err != nil {
				_ = rows.Close()
				return UnitRef{}, err
			}
			candidates = append(candidates, v)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return UnitRef{}, err
		}
	}
	var eligible []queuedClaim
	for _, candidate := range candidates {
		profile, requirements, err := matchingInputsTx(ctx, tx, demand, candidate.unit)
		if err != nil {
			var denied *apierr.Error
			if errors.As(err, &denied) && denied.Code == apierr.Forbidden {
				continue
			}
			return UnitRef{}, err
		}
		verdict := match.Evaluate(profile, requirements)
		if !verdict.OK() || !match.Fits(profile.Capacity, demand.Available, requirements.Request).OK() {
			continue
		}
		candidate.score = verdict.PreferenceScore
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		return UnitRef{}, notFound("eligible work", "")
	}
	slices.SortStableFunc(eligible, func(a, b queuedClaim) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		if a.created < b.created {
			return -1
		}
		if a.created > b.created {
			return 1
		}
		if a.unit.RunID < b.unit.RunID {
			return -1
		}
		if a.unit.RunID > b.unit.RunID {
			return 1
		}
		if a.unit.NodeID < b.unit.NodeID {
			return -1
		}
		if a.unit.NodeID > b.unit.NodeID {
			return 1
		}
		return 0
	})
	return eligible[0].unit, nil
}

func queuedClaimQuery(c capability.Capability, kind capability.UnitKind, now int64) (string, []any) {
	var query string
	args := []any{}
	switch kind {
	case capability.UnitNode:
		query = `SELECT r.id,n.node_id,0,r.created_at FROM runs r JOIN nodes n ON n.team=r.team AND n.run_id=r.id WHERE n.status='pending' AND n.ready_at IS NOT NULL AND n.ready_at<=? AND n.claimed_by IS NULL AND n.cancel_requested_at IS NULL AND (r.claim_wait_ns<=0 OR n.queue_started_at>?-r.claim_wait_ns)`
		args = append(args, now, now)
	case capability.UnitEval:
		query = `SELECT r.id,e.source_node_id,e.source_generation,r.created_at FROM runs r JOIN eval_units e ON e.team=r.team AND e.run_id=r.id JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id JOIN leases l ON l.name=e.owner_lease_name WHERE e.status='pending' AND e.claimed_by IS NULL AND (e.expires_at IS NULL OR e.expires_at>?) AND l.name='controller.owner' AND l.until>? AND n.generation=e.source_generation AND n.outcome IN ('success','memoized') AND n.expansion='expected'`
		args = append(args, now, now)
	default:
		query = `SELECT r.id,'',0,r.created_at FROM runs r JOIN triggers t ON t.team=r.team AND t.id=r.id WHERE t.unit_kind=? AND t.status='pending' AND t.claimed_by IS NULL AND t.cancel_requested_at IS NULL AND t.available_at<=? AND (t.started_at IS NOT NULL OR r.claim_wait_ns<=0 OR r.created_at>?-r.claim_wait_ns)`
		args = append(args, string(kind), now, now)
	}
	query += ` AND r.status IN ('pending','running') AND r.cancel_requested_at IS NULL AND r.intake_mode='execute' AND NOT EXISTS (SELECT 1 FROM triggers deadline WHERE deadline.team=r.team AND deadline.id=r.id AND deadline.expires_at<=?)`
	args = append(args, now)
	if !launcherClaimant(c) {
		query += ` AND r.team=?`
		args = append(args, c.Team)
	}
	if c.Binding.RunID != "" {
		query += ` AND r.id=?`
		args = append(args, c.Binding.RunID)
	}
	return query, args
}
