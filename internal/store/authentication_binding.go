package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func authenticationBindingTx(ctx context.Context, tx *storeTx, c capability.Capability) (capability.BindingState, error) {
	out := capability.BindingState{Current: c.Binding}
	if c.Binding.RunID == "" {
		return out, nil
	}
	var cancel, finished sql.NullInt64
	var reason string
	if err := tx.QueryRowContext(ctx, `SELECT cancel_requested_at,cancel_reason,finished_at FROM runs WHERE team=? AND id=?`, c.Team, c.Binding.RunID).Scan(&cancel, &reason, &finished); err != nil {
		return out, err
	}
	out.RunCancelled, out.RunFinished = cancel.Valid, finished.Valid
	if cancel.Valid {
		out.CancelReason = reason
	}
	switch c.Binding.Kind {
	case capability.BindingTrigger:
		if err := authenticationOwnerTx(ctx, tx, c, &out); err != nil {
			return out, err
		}
	case capability.BindingNode:
		if err := authenticationNodeTx(ctx, tx, c, &out); err != nil {
			return out, err
		}
	case capability.BindingRun:
		if c.Unit == capability.UnitPlan {
			if err := authenticationOwnerTx(ctx, tx, c, &out); err != nil {
				return out, err
			}
		}
	}
	if c.Binding.Expansion != nil {
		if err := authenticationExpansionTx(ctx, tx, c, &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func authenticationOwnerTx(ctx context.Context, tx *storeTx, c capability.Capability, out *capability.BindingState) error {
	var lease, finished, cancelled sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(claimed_by,''),generation,lease_expires_at,finished_at,cancel_requested_at FROM triggers WHERE team=? AND id=?`, c.Team, c.Binding.RunID).Scan(&out.Current.Holder, &out.Current.Generation, &lease, &finished, &cancelled)
	if err != nil {
		return err
	}
	out.HereClaimed = out.Current.Generation > 0
	out.RunCancelled = out.RunCancelled || cancelled.Valid
	out.FinishedAt = stateTime(finished)
	if lease.Valid {
		out.LeaseExpiresAt = *stateTime(lease)
	}
	if finished.Valid && out.Current.Generation == c.Binding.Generation {
		out.Current.Holder = c.Binding.Holder
	}
	if c.Unit != "" {
		var sealed int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_streams WHERE team=? AND run_id=? AND subject_kind='owner_unit' AND unit_kind=? AND generation=? AND sealed_at IS NOT NULL`, c.Team, c.Binding.RunID, c.Unit, out.Current.Generation).Scan(&sealed)
		out.LogSealed = sealed > 0
	}
	return err
}

func authenticationNodeTx(ctx context.Context, tx *storeTx, c capability.Capability, out *capability.BindingState) error {
	var lease, started, finished, cancelled sql.NullInt64
	var reason, status string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(claimed_by,''),generation,attempt,lease_expires_at,started_at,finished_at,cancel_requested_at,cancel_reason,status FROM nodes WHERE team=? AND run_id=? AND node_id=?`, c.Team, c.Binding.RunID, c.Binding.NodeID).Scan(&out.Current.Holder, &out.Current.Generation, &out.Current.Attempt, &lease, &started, &finished, &cancelled, &reason, &status)
	if err != nil {
		return err
	}
	if c.Unit == capability.UnitNode && status == string(StatusPending) && !started.Valid {
		out.Current.Attempt = 0
	}
	out.FinishedAt = stateTime(finished)
	out.RunCancelled = out.RunCancelled || cancelled.Valid
	if cancelled.Valid {
		out.CancelReason = reason
	}
	if lease.Valid {
		out.LeaseExpiresAt = *stateTime(lease)
	}
	if finished.Valid && out.Current.Generation == c.Binding.Generation {
		out.Current.Holder = c.Binding.Holder
		if out.Current.Attempt > 0 {
			err = tx.QueryRowContext(ctx, `SELECT holder FROM node_attempts WHERE team=? AND run_id=? AND node_id=? AND generation=? AND attempt=?`, c.Team, c.Binding.RunID, c.Binding.NodeID, out.Current.Generation, out.Current.Attempt).Scan(&out.Current.Holder)
			if err != nil {
				return err
			}
		}
	}
	if c.Unit == capability.UnitNode {
		// Logical admission alone cannot revive an expired or completed execution award.
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admission_work a JOIN nodes n ON n.team=a.team AND n.run_id=a.run_id AND n.node_id=a.node_id JOIN runs r ON r.team=n.team AND r.id=n.run_id WHERE a.team=? AND a.run_id=? AND a.node_id=? AND a.generation=? AND a.holder=? AND a.granted=1 AND a.lease_expires_at>? AND n.generation=a.generation AND n.claimed_by=a.holder AND n.status='running' AND n.lease_expires_at>? AND n.cancel_requested_at IS NULL AND r.cancel_requested_at IS NULL AND r.finished_at IS NULL)`, c.Team, c.Binding.RunID, c.Binding.NodeID, c.Binding.Generation, c.Binding.Holder, tx.now(ctx).UnixNano(), tx.now(ctx).UnixNano()).Scan(&out.StartPermitted)
		if err != nil {
			return err
		}
	}
	var sealed int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_streams WHERE team=? AND run_id=? AND subject_kind='job_attempt' AND node_id=? AND generation=? AND attempt=? AND sealed_at IS NOT NULL`, c.Team, c.Binding.RunID, c.Binding.NodeID, out.Current.Generation, out.Current.Attempt).Scan(&sealed)
	out.LogSealed = sealed > 0
	return err
}

func authenticationExpansionTx(ctx context.Context, tx *storeTx, c capability.Capability, out *capability.BindingState) error {
	expansion := &capability.ExpansionState{Lease: *c.Binding.Expansion}
	var lease sql.NullInt64
	if expansion.Lease.Kind == capability.UnitTrigger {
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(claimed_by,''),generation,lease_expires_at FROM triggers WHERE team=? AND id=?`, c.Team, c.Binding.RunID).Scan(&expansion.Lease.Holder, &expansion.Lease.Generation, &lease)
		if err != nil {
			return err
		}
	} else if expansion.Lease.Kind == capability.UnitEval {
		row, err := readNodeTx(ctx, tx, Team(c.Team), controllerwire.NodeRef{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID)})
		if err != nil {
			return err
		}
		expansion.ApprovalPredicate = pendingApprovalPredicate(row)
		var owner string
		var generation int64
		var current, until sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(e.claimed_by,''),e.generation,e.lease_expires_at,e.owner_lease_name,e.owner_generation,l.generation,l.until FROM eval_units e LEFT JOIN leases l ON l.name=e.owner_lease_name WHERE e.team=? AND e.run_id=? AND e.source_node_id=? AND e.source_generation=?`, c.Team, c.Binding.RunID, c.Binding.NodeID, c.Binding.Generation).Scan(&expansion.Lease.Holder, &expansion.Lease.Generation, &lease, &owner, &generation, &current, &until)
		if err != nil {
			return err
		}
		if owner == "controller.owner" && current.Valid && current.Int64 == generation {
			expansion.ControllerLeaseExpiresAt = stateTime(until)
		}
		var sealed int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_streams WHERE team=? AND run_id=? AND subject_kind='owner_unit' AND unit_kind='eval' AND source_node_id=? AND source_generation=? AND generation=? AND sealed_at IS NOT NULL`, c.Team, c.Binding.RunID, c.Binding.NodeID, c.Binding.Generation, expansion.Lease.Generation).Scan(&sealed)
		if err != nil {
			return err
		}
		out.LogSealed = sealed > 0
	} else {
		return errors.New("unknown expansion lease kind")
	}
	if lease.Valid {
		expansion.ExpiresAt = *stateTime(lease)
	}
	out.Expansion = expansion
	return nil
}
