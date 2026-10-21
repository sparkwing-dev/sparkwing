package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type ownerRecovery struct {
	status, prefix                      string
	generation                          int64
	disruptions                         int
	lease, deadline, started, cancelled sql.NullInt64
}

func reapOwnerUnitTx(ctx context.Context, tx *storeTx, target expiredUnit, actor Actor, now time.Time) (*ReapedUnit, error) {
	var owner ownerRecovery
	err := tx.QueryRowContext(ctx, `SELECT status,capability_prefix,generation,disruptions,lease_expires_at,expires_at,started_at,cancel_requested_at FROM triggers WHERE team=? AND id=?`+tx.forUpdate(), target.team, target.ref.RunID).Scan(&owner.status, &owner.prefix, &owner.generation, &owner.disruptions, &owner.lease, &owner.deadline, &owner.started, &owner.cancelled)
	if err != nil {
		return nil, err
	}
	if err := lockStateRunTx(ctx, tx, target.team, string(target.ref.RunID)); err != nil {
		return nil, err
	}
	run, err := readRunTx(ctx, tx, target.team, string(target.ref.RunID))
	if err != nil {
		return nil, err
	}
	if run.FinishedAt != nil {
		return nil, nil
	}
	if run.Dispatch == string(runspec.DispatchController) {
		var accepted int64
		if err := tx.QueryRowContext(ctx, `SELECT plan_accepted_generation FROM runs WHERE team=? AND id=?`, target.team, run.ID).Scan(&accepted); err != nil {
			return nil, err
		}
		if accepted > 0 {
			return nil, nil
		}
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return nil, clockErr1
	}
	now = clockTime1
	if owner.status == "claimed" && owner.lease.Valid && owner.lease.Int64 > now.UnixNano() {
		return nil, nil
	}
	award, err := unitBillingAwardTx(ctx, tx, target.team, UnitRef{Kind: target.kind, RunID: target.ref.RunID}, owner.generation)
	if err != nil {
		return nil, err
	}
	stopped := award != nil && award.stop != ""
	cancelled := owner.cancelled.Valid || run.CancelRequestedAt != nil
	expired := owner.deadline.Valid && owner.deadline.Int64 <= now.UnixNano()
	lapsed := owner.status == "claimed" && owner.lease.Valid && owner.lease.Int64 <= now.UnixNano()
	wait := runClaimWait(run)
	queuedOut := !owner.started.Valid && wait.Set && wait.Value > 0 && !run.CreatedAt.Add(wait.Value).After(now)
	if !cancelled && !expired && !lapsed && !queuedOut && owner.status != "failed" && owner.status != "done" {
		return nil, nil
	}
	if lapsed || expired {
		tx.finishedLocalSecrets = append(tx.finishedLocalSecrets, localSecretKey{target.team, run.ID})
	}
	if lapsed && owner.started.Valid && !cancelled && !stopped && owner.disruptions < limits.MaxDisruptions {
		owner.disruptions++
	}
	failed := stopped || owner.status == "failed" || expired || queuedOut || owner.disruptions >= limits.MaxDisruptions || lapsed && owner.started.Valid && run.Request.Request.Origin == runspec.OriginLocal
	result := &ReapedUnit{Team: target.team, RunID: string(target.ref.RunID), Kind: string(target.kind), Generation: owner.generation, Disruptions: owner.disruptions, Requeued: !cancelled && !failed, Cancelled: cancelled, Failed: failed && !cancelled}
	if result.Requeued {
		return result, writeOwnerRecoveryTx(ctx, tx, target, owner, "pending", actor, now)
	}
	status := "failed"
	if cancelled {
		status = "done"
	}
	if err := writeOwnerRecoveryTx(ctx, tx, target, owner, status, actor, now); err != nil {
		return nil, err
	}
	if cancelled && run.CancelRequestedAt == nil {
		if _, err := transitionRunTx(ctx, tx, target.team, RunTransition{RunID: runspec.RunID(run.ID), To: RunCancelled, Cause: RunCancel, Actor: actor}); err != nil {
			return nil, err
		}
	}
	if err := cancelRunNodesTx(ctx, tx, target.team, run.ID, actor, run.CancelReason, now); err != nil {
		return nil, err
	}
	var claims int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE team=? AND run_id=? AND status<>'done' AND claimed_by IS NOT NULL`, target.team, run.ID).Scan(&claims); err != nil {
		return nil, err
	}
	if claims != 0 {
		return result, nil
	}
	to, reason, message := RunFailed, "agent_lost", "owner execution lost"
	cause := RunOwnerLost
	if cancelled {
		to, reason, message = RunCancelled, "cancelled", ""
	} else if stopped {
		reason, message = award.stop, ""
	} else if expired {
		reason, message = "timeout", "owner execution deadline exceeded"
		if run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere {
			message = "here runner did not return"
		}
	} else if queuedOut {
		reason, message = "queue_timeout", "owner queue deadline exceeded"
		if run.State == RunPending {
			cause = RunQueueTimeout
		}
	}
	_, err = transitionRunTx(ctx, tx, target.team, RunTransition{RunID: runspec.RunID(run.ID), To: to, Actor: actor, Cause: cause, Reason: reason, MaskedError: message})
	return result, err
}

func reapEvalUnitTx(ctx context.Context, tx *storeTx, target expiredUnit, actor Actor, now time.Time) (*ReapedUnit, error) {
	var unit ownerRecovery
	err := tx.QueryRowContext(ctx, `SELECT status,capability_prefix,generation,disruptions,lease_expires_at,expires_at,started_at FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`+tx.forUpdate(), target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration).Scan(&unit.status, &unit.prefix, &unit.generation, &unit.disruptions, &unit.lease, &unit.deadline, &unit.started)
	if err != nil {
		return nil, err
	}
	if unit.status != "pending" && unit.status != "claimed" && unit.status != "running" {
		return nil, nil
	}
	if err := lockStateRunTx(ctx, tx, target.team, string(target.ref.RunID)); err != nil {
		return nil, err
	}
	run, err := readRunTx(ctx, tx, target.team, string(target.ref.RunID))
	if err != nil {
		return nil, err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return nil, clockErr2
	}
	now = clockTime2
	if unit.lease.Valid && unit.lease.Int64 > now.UnixNano() {
		return nil, nil
	}
	source, err := readNodeTx(ctx, tx, target.team, target.ref)
	if err != nil {
		return nil, err
	}
	if source.Generation != runspec.ClaimGeneration(target.sourceGeneration) {
		return nil, holderRefusal()
	}
	if source.Expansion != ExpansionExpected {
		status := "done"
		if source.Expansion == ExpansionFailed {
			status = "failed"
		}
		if source.Expansion == ExpansionNone {
			status = "cancelled"
		}
		if err := writeEvalRecoveryTx(ctx, tx, target, unit, status, now); err != nil {
			return nil, err
		}
		return &ReapedUnit{Team: target.team, RunID: string(target.ref.RunID), NodeID: string(target.ref.NodeID), Kind: "eval", Generation: unit.generation, Disruptions: unit.disruptions, Cancelled: status == "cancelled", Failed: status == "failed"}, settleControllerTx(ctx, tx, target.team, string(target.ref.RunID), now)
	}
	award, err := unitBillingAwardTx(ctx, tx, target.team, UnitRef{Kind: capability.UnitEval, RunID: target.ref.RunID, NodeID: target.ref.NodeID, SourceGeneration: target.sourceGeneration}, unit.generation)
	if err != nil {
		return nil, err
	}
	stopped := award != nil && award.stop != ""
	cancelled := run.CancelRequestedAt != nil
	expired := unit.deadline.Valid && unit.deadline.Int64 <= now.UnixNano()
	lapsed := unit.lease.Valid && unit.lease.Int64 <= now.UnixNano()
	if !cancelled && !expired && !lapsed {
		return nil, nil
	}
	if lapsed && unit.started.Valid && !cancelled && !stopped && unit.disruptions < limits.MaxDisruptions {
		unit.disruptions++
	}
	status := "pending"
	if stopped || expired || unit.disruptions >= limits.MaxDisruptions {
		status = "failed"
	}
	if cancelled {
		status = "cancelled"
	}
	if err := writeEvalRecoveryTx(ctx, tx, target, unit, status, now); err != nil {
		return nil, err
	}
	if status != "pending" {
		if err := resolveReapedExpansionTx(ctx, tx, target, status, actor, now); err != nil {
			return nil, err
		}
	}
	return &ReapedUnit{Team: target.team, RunID: string(target.ref.RunID), NodeID: string(target.ref.NodeID), Kind: "eval", Generation: unit.generation, Disruptions: unit.disruptions, Requeued: status == "pending", Cancelled: status == "cancelled", Failed: status == "failed"}, nil
}
