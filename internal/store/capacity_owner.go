package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type capacityFence struct {
	binding runstate.CapacityBinding
	actor   runstate.Actor
}

func checkCapacityFenceTx(ctx context.Context, tx *storeTx, team Team, key string, fence capacityFence) error {
	if err := checkCapacityUnitTx(ctx, tx, team, fence); err != nil {
		return err
	}
	b := fence.binding
	_, err := declaredCapacityGroupTx(ctx, tx, team, b.RunID, b.NodeID, key, fence.actor.Authority)
	return err
}

func checkCapacityUnitTx(ctx context.Context, tx *storeTx, team Team, fence capacityFence) error {
	b := fence.binding
	if b.RunID == "" || b.Generation < 0 {
		return holderRefusal()
	}
	if err := checkCapacityOwnerTx(ctx, tx, team, fence); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, team, b.RunID); err != nil {
		return err
	}
	var status string
	var cancelled sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT status,cancel_requested_at FROM runs WHERE team=? AND id=?`, team, b.RunID).Scan(&status, &cancelled); err != nil {
		return err
	}
	if cancelled.Valid || status != string(runstate.RunPending) && status != string(runstate.RunRunning) {
		return holderRefusal()
	}
	if b.NodeID == "" {
		if b.Generation != int64(fence.actor.Owner.Generation) {
			return holderRefusal()
		}
	} else {
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT status,generation,cancel_requested_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), team, b.RunID, b.NodeID).Scan(&status, &generation, &cancelled); err != nil {
			return err
		}
		if status == string(runstate.StatusDone) || cancelled.Valid || generation != b.Generation {
			return holderRefusal()
		}
	}

	return nil
}

func checkCapacityMutationTx(ctx context.Context, tx *storeTx, team Team, key, id string, fence *capacityFence) error {
	if fence == nil {
		return nil
	}
	if err := checkCapacityFenceTx(ctx, tx, team, key, *fence); err != nil {
		return err
	}
	b := fence.binding
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		rows, err := tx.queryContext(ctx, `SELECT run_id,node_id,generation FROM `+table+` WHERE team=? AND key=? AND ((run_id=? AND node_id=?) OR holder_id=?)`, team, key, b.RunID, b.NodeID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var current runstate.CapacityBinding
			if err := rows.Scan(&current.RunID, &current.NodeID, &current.Generation); err != nil {
				return errors.Join(err, rows.Close())
			}
			if current.RunID != b.RunID || current.NodeID != b.NodeID {
				_ = rows.Close()
				return holderRefusal()
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}

func capacityQueueDeadlineTx(ctx context.Context, tx *storeTx, team Team, waiter ConcurrencyWaiter) (time.Time, error) {
	level, scope, _, name, ok := runspec.ParseConcurrencyKey(waiter.Key)
	if !ok || (waiter.NodeID == "") != (level == "plan") {
		return time.Time{}, nil
	}
	groups, err := capacityGroupsTx(ctx, tx, team, waiter.RunID, waiter.NodeID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	for _, group := range groups {
		if group.Group == name && group.Scope == scope && group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0 {
			return waiter.ArrivedAt.Add(time.Duration(*group.QueueTimeoutMS) * time.Millisecond), nil
		}
	}
	return time.Time{}, nil
}

func cancelSupersededCapacityTx(ctx context.Context, tx *storeTx, team Team, key, id string, actor runstate.Actor) error {
	var b runstate.CapacityBinding
	err := tx.QueryRowContext(ctx, `SELECT run_id,node_id,generation FROM concurrency_holders WHERE team=? AND key=? AND holder_id=? AND superseded=1`, team, key, id).Scan(&b.RunID, &b.NodeID, &b.Generation)
	if err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, team, b.RunID); err != nil {
		return err
	}
	now := tx.now(ctx)
	if b.NodeID != "" {
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(b.RunID), NodeID: runspec.NodeID(b.NodeID)})
		if err != nil {
			return err
		}
		// Reservations survive award replacement, so supersession targets the current job.
		if row.Status == runstate.StatusDone || row.CancelRequestedAt != nil {
			return nil
		}
		row.CancelRequestedAt = &now
		row.CancelReason = "concurrency superseded"
		row.StateSeq++
		row.StateActor = actor
		row.StateDigest, err = stateHash(struct {
			Key, ID string
			Binding runstate.CapacityBinding
			Actor   runstate.Actor
		}{key, id, b, actor})
		if err != nil {
			return err
		}
		if err := writeNodeStateTx(ctx, tx, team, row); err != nil {
			return err
		}
		if err := clampRunLeasesTx(ctx, tx, team, b.RunID, b.NodeID); err != nil {
			return err
		}
		return appendStateEventTx(ctx, tx, team, row.Ref, "job_cancel_requested", row.StateSeq, nil, now)
	}
	run, err := readRunTx(ctx, tx, team, b.RunID)
	if err != nil {
		return err
	}
	if run.FinishedAt != nil || run.CancelRequestedAt != nil {
		return nil
	}
	result, err := tx.execContext(ctx, `UPDATE runs SET cancel_requested_at=?,cancel_reason='concurrency superseded',state_seq=state_seq+1 WHERE team=? AND id=? AND state_seq=? AND finished_at IS NULL`, now.UnixNano(), team, b.RunID, run.StateSeq)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return holderRefusal()
	}
	if err := clampRunLeasesTx(ctx, tx, team, b.RunID, b.NodeID); err != nil {
		return err
	}
	return appendStateEventTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(b.RunID)}, "run_cancel_requested", run.StateSeq+1, nil, now)
}

func capacityWaiterCurrentTx(ctx context.Context, tx *storeTx, team Team, waiter ConcurrencyWaiter) (bool, error) {
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT lease_expires_at FROM concurrency_waiters WHERE team=? AND key=? AND run_id=? AND node_id=?`, team, waiter.Key, waiter.RunID, waiter.NodeID).Scan(&expires); err != nil {
		return false, err
	}
	live, err := capacityWorkLiveTx(ctx, tx, team, runstate.CapacityBinding{RunID: waiter.RunID, NodeID: waiter.NodeID}, expires)
	if err != nil || live {
		return live, err
	}
	return false, discardAdmissionLevelTx(ctx, tx, team, waiter.RunID, waiter.NodeID)
}

func capacityWorkLiveTx(ctx context.Context, tx *storeTx, team Team, binding runstate.CapacityBinding, expires int64) (bool, error) {
	if expires <= tx.now(ctx).UnixNano() {
		return false, nil
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r WHERE team=? AND id=? AND status IN ('pending','running') AND finished_at IS NULL AND (?='' OR EXISTS(SELECT 1 FROM nodes n WHERE n.team=r.team AND n.run_id=r.id AND n.node_id=? AND n.status<>'done')))`, team, binding.RunID, binding.NodeID, binding.NodeID).Scan(&active); err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	ceiling, err := capacityLeaseCeilingTx(ctx, tx, team, binding.RunID, binding.NodeID, expires)
	var refusal *ErrIllegalTransition
	if errors.As(err, &refusal) && refusal.Reason == ReasonHolderRights {
		return false, nil
	}
	return ceiling > tx.now(ctx).UnixNano(), err
}

func lockAdmissionTeamTx(ctx context.Context, tx *storeTx, team Team) error {
	if tx.dialect == DialectPostgres {
		var locked bool
		if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(?,0))`, "admission:"+string(team)).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return errCapacityAdmissionBusy
		}
	}
	return nil
}

func prepareCapacityAcquisitionTx(ctx context.Context, tx *storeTx, team Team, keys []string, fence capacityFence) error {
	if err := lockAdmissionTeamTx(ctx, tx, team); err != nil {
		return err
	}
	for _, key := range keys {
		name := fmt.Sprintf("capacity-admission:%d:%s:%s", len(team), team, key)
		// safety: settlement already owns its run row, so it must never wait behind admission that needs that row.
		if tx.dialect == DialectPostgres {
			var locked bool
			if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(?,0))`, name).Scan(&locked); err != nil {
				return err
			}
			if !locked {
				return errCapacityAdmissionBusy
			}
		} else if err := advisoryLockTx(ctx, tx, name); err != nil {
			return err
		}
	}
	if fence.actor.Authority.Kind != "" {
		if _, err := authorityChainTx(ctx, tx, fence.actor.Authority, true); err != nil {
			return err
		}
	}
	if err := checkCapacityOwnerTx(ctx, tx, team, fence); err != nil {
		return err
	}
	if err := lockCapacityWorkTx(ctx, tx, team, keys, runspec.NodeRef{RunID: runspec.RunID(fence.binding.RunID), NodeID: runspec.NodeID(fence.binding.NodeID)}, true); err != nil {
		return err
	}
	return checkCapacityUnitTx(ctx, tx, team, fence)
}

func lockCapacityWorkTx(ctx context.Context, tx *storeTx, team Team, keys []string, ref runspec.NodeRef, nonblocking bool) error {
	predicate := "NULL"
	var args []any
	if len(keys) > 0 {
		predicate = strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	}
	for range 2 {
		args = append(args, team)
		for _, key := range keys {
			args = append(args, key)
		}
	}
	args = append(args, ref.RunID, ref.NodeID)
	rows, err := tx.queryContext(ctx, `SELECT run_id,node_id FROM concurrency_holders WHERE team=? AND key IN (`+predicate+`)
UNION SELECT run_id,node_id FROM concurrency_waiters WHERE team=? AND key IN (`+predicate+`)
UNION SELECT ?,? ORDER BY run_id,node_id`, args...)
	if err != nil {
		return err
	}
	var work []runspec.NodeRef
	for rows.Next() {
		var ref runspec.NodeRef
		if err := rows.Scan(&ref.RunID, &ref.NodeID); err != nil {
			return errors.Join(err, rows.Close())
		}
		work = append(work, ref)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, ref := range work {
		if err := lockWorkRowTx(ctx, tx, storeLockKey{tier: lockRun, keys: [3]string{string(team), string(ref.RunID)}}, nonblocking); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	for _, ref := range work {
		if ref.NodeID == "" {
			continue
		}
		if err := lockWorkRowTx(ctx, tx, storeLockKey{tier: lockNode, keys: [3]string{string(team), string(ref.RunID), string(ref.NodeID)}}, nonblocking); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func checkCapacityOwnerTx(ctx context.Context, tx *storeTx, team Team, fence capacityFence) error {
	binding := fence.binding
	if fence.actor.Kind == runstate.ActorHolder {
		if binding.NodeID == "" || runspec.ClaimGeneration(binding.Generation) != fence.actor.Generation {
			return holderRefusal()
		}
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)})
		if err != nil {
			return err
		}
		return checkHolderTx(ctx, tx, team, row, fence.actor)
	}
	check := checkOwnerTx
	if binding.NodeID == "" {
		check = checkOwnerLeaseTx
	}
	return check(ctx, tx, team, fence.actor, binding.RunID)
}
