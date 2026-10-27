package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// ReapUnits recovers expired work.
func (o *Operator) ReapUnits(ctx context.Context, limit int) ([]ReapedUnit, error) {
	if limit <= 0 {
		return nil, errors.New("reaper limit must be positive")
	}
	targets, err := o.expiredUnits(ctx, limit)
	if err != nil {
		return nil, err
	}
	var out []ReapedUnit
	var failures error
	for _, target := range targets {
		var result *ReapedUnit
		err := o.s.inTx(ctx, func(tx *storeTx) error {
			now := tx.now(ctx)
			actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
			var err error
			if target.kind == capability.UnitNode {
				result, err = reapNodeUnitTx(ctx, tx, target, actor, now)
			} else if target.kind == capability.UnitEval {
				result, err = reapEvalUnitTx(ctx, tx, target, actor, now)
			} else {
				result, err = reapOwnerUnitTx(ctx, tx, target, actor, now)
			}
			return err
		})
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("reap %s/%s/%s: %w", target.team, target.ref.RunID, target.ref.NodeID, err))
			continue
		}
		if result != nil {
			out = append(out, *result)
		}
	}
	return out, failures
}

type expiredUnit struct {
	team             Team
	ref              runspec.NodeRef
	kind             capability.UnitKind
	sourceGeneration int64
}

func (o *Operator) expiredUnits(ctx context.Context, limit int) ([]expiredUnit, error) {
	current, err := o.s.databaseNow(ctx)
	if err != nil {
		return nil, err
	}
	now := current.UnixNano()
	rows, err := o.s.query(ctx, `SELECT team,run_id,node_id,kind,source_generation FROM (
	 SELECT team,run_id,node_id,'node' AS kind,0 AS source_generation,lease_expires_at AS due FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at<=? AND status<>'done'
 UNION ALL
	 SELECT n.team,n.run_id,n.node_id,'node',0,n.queue_started_at FROM nodes n
	 JOIN runs r ON r.team=n.team AND r.id=n.run_id
	 WHERE n.status='pending' AND n.claimed_by IS NULL AND n.queue_started_at IS NOT NULL
	 AND r.claim_wait_ns>0 AND n.queue_started_at<=?-r.claim_wait_ns
	 UNION ALL
	 SELECT n.team,n.run_id,n.node_id,'node',0,a.requested_at FROM nodes n
	 JOIN approvals a ON a.team=n.team AND a.run_id=n.run_id AND a.node_id=n.node_id
	 WHERE n.status='approval_pending' AND a.resolved_at IS NULL AND a.timeout_ms>0
	 AND a.requested_at<=?-a.timeout_ms*1000000
	 UNION ALL
	 SELECT t.team,t.id,'',t.unit_kind,0,COALESCE(t.lease_expires_at,t.expires_at,t.created_at) AS due FROM triggers t
 JOIN runs r ON r.team=t.team AND r.id=t.id
	 WHERE r.status IN ('pending','running') AND NOT (r.dispatch='controller' AND r.plan_accepted_generation>0) AND (
 (t.status='claimed' AND t.lease_expires_at<=?) OR
 (t.status IN ('pending','claimed') AND t.expires_at<=?) OR
	 (t.status='pending' AND r.claim_wait_ns>0 AND r.created_at<=?-r.claim_wait_ns) OR
	 t.status='failed' OR r.cancel_requested_at IS NOT NULL OR t.cancel_requested_at IS NOT NULL)
	 UNION ALL
	 SELECT e.team,e.run_id,e.source_node_id,'eval',e.source_generation,COALESCE(e.lease_expires_at,e.expires_at,0) AS due FROM eval_units e
	 JOIN runs r ON r.team=e.team AND r.id=e.run_id
	 WHERE e.status IN ('pending','claimed','running') AND (
	 (e.claimed_by IS NOT NULL AND e.lease_expires_at<=?) OR e.expires_at<=? OR r.cancel_requested_at IS NOT NULL)
	 ) AS expired ORDER BY CASE WHEN kind='node' THEN 0 WHEN kind='eval' THEN 1 ELSE 2 END,due,team,run_id,node_id LIMIT ?`, now, now, now, now, now, now, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []expiredUnit
	for rows.Next() {
		var target expiredUnit
		if err := rows.Scan(&target.team, &target.ref.RunID, &target.ref.NodeID, &target.kind, &target.sourceGeneration); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func reapNodeUnitTx(ctx context.Context, tx *storeTx, target expiredUnit, actor runstate.Actor, now time.Time) (*ReapedUnit, error) {
	if err := lockStateRunTx(ctx, tx, target.team, string(target.ref.RunID)); err != nil {
		return nil, err
	}
	row, err := readNodeTx(ctx, tx, target.team, target.ref)
	if err != nil {
		return nil, err
	}
	now = tx.now(ctx)
	if row.Status == runstate.StatusDone {
		return nil, nil
	}
	run, err := readRunTx(ctx, tx, target.team, string(target.ref.RunID))
	if err != nil {
		return nil, err
	}
	if row.ClaimedBy == "" {
		return reapUnclaimedNodeTx(ctx, tx, target, actor, row, run, now)
	}
	if row.LeaseExpiresAt == nil || row.LeaseExpiresAt.After(now) {
		return nil, nil
	}
	to := nodePending
	if row.CancelRequestedAt != nil || run.CancelRequestedAt != nil {
		to = runstate.NodeCancelled
	} else if row.Status == runstate.StatusRunning && row.Disruptions+1 >= limits.MaxDisruptions {
		to = runstate.NodeFailed
	}
	// A lease that ran out because its award was stopped, such as at the requester's overflow allowance, ends the job
	// with that reason, the way a funding refusal does, rather than requeueing it as a generic lapse.
	var stop string
	err = tx.QueryRowContext(ctx, `SELECT stop_reason FROM billing_awards WHERE team=? AND run_id=? AND node_id=? AND unit_kind=? AND generation=?`,
		target.team, target.ref.RunID, target.ref.NodeID, capability.UnitNode, int64(row.Generation)).Scan(&stop)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if stop != "" && to != runstate.NodeCancelled {
		to = runstate.NodeCancelled
		previous := tx.cancelingTeam
		tx.cancelingTeam = target.team
		_, err = transitionNodeStateTx(ctx, tx, target.team, nodeTransition{Ref: target.ref, To: to, Actor: runstate.Actor{Kind: runstate.ActorOperator},
			Cause: causeCancel, Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: to, Reason: stop}}})
		tx.cancelingTeam = previous
	} else {
		_, err = transitionNodeTx(ctx, tx, target.team, nodeTransition{Ref: target.ref, To: to, Actor: actor, Cause: runstate.CauseLeaseLapse})
	}
	if err != nil {
		return nil, err
	}
	disruptions := row.Disruptions
	if row.Status == runstate.StatusRunning && to != runstate.NodeCancelled {
		disruptions++
	}
	return &ReapedUnit{Team: target.team, RunID: string(target.ref.RunID), NodeID: string(target.ref.NodeID), Kind: string(target.kind), Generation: int64(row.Generation), Disruptions: disruptions, Requeued: to == nodePending, Cancelled: to == runstate.NodeCancelled, Failed: to == runstate.NodeFailed}, nil
}

func reapUnclaimedNodeTx(ctx context.Context, tx *storeTx, target expiredUnit, actor runstate.Actor, row runstate.NodeRow, run RunRow, now time.Time) (*ReapedUnit, error) {
	to, cause := runstate.NodeFailed, runstate.CauseQueueTimeout
	if row.Status == runstate.StatusApprovalPending {
		var policy string
		var requested, timeout int64
		var resolved sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT on_timeout,requested_at,timeout_ms,resolved_at FROM approvals WHERE team=? AND run_id=? AND node_id=?`, target.team, target.ref.RunID, target.ref.NodeID).Scan(&policy, &requested, &timeout, &resolved)
		if err != nil {
			return nil, err
		}
		if resolved.Valid || timeout <= 0 || time.Unix(0, requested).Add(time.Duration(timeout)*time.Millisecond).After(now) {
			return nil, nil
		}
		cause = causeApprovalTimeout
		to = approvalTimeoutOutcome(policy)
	} else {
		wait := runClaimWait(run)
		if row.Status != runstate.StatusPending || row.QueueStartedAt == nil || !wait.Set || wait.Value <= 0 || row.QueueStartedAt.Add(wait.Value).After(now) {
			return nil, nil
		}
	}
	if row.CancelRequestedAt != nil || run.CancelRequestedAt != nil {
		to, cause = runstate.NodeCancelled, causeCancel
	}
	change := nodeTransition{Ref: target.ref, To: to, Cause: cause, Actor: actor}
	if to == runstate.NodeFailed {
		change.Extra.Result = &runstate.NodeResult{Outcome: to, Reason: string(cause)}
	}
	if _, err := transitionNodeTx(ctx, tx, target.team, change); err != nil {
		return nil, err
	}
	return &ReapedUnit{Team: target.team, RunID: string(target.ref.RunID), NodeID: string(target.ref.NodeID), Kind: "node", Generation: int64(row.Generation), Disruptions: row.Disruptions, Failed: to == runstate.NodeFailed, Cancelled: to == runstate.NodeCancelled}, nil
}
