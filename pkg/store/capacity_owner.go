package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type capacityFence struct {
	binding CapacityBinding
	actor   Actor
}

// AcquireCapacity admits capacity under the live owner and captured work binding.
func (t *Tenant) AcquireCapacity(ctx context.Context, req AcquireSlotRequest, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	if req.RunID != binding.RunID || req.NodeID != binding.NodeID || req.CacheKeyHash != "" || req.InheritedHolderID != "" {
		return AcquireSlotResponse{}, holderRefusal()
	}
	return t.acquireConcurrencySlot(ctx, req, &capacityFence{binding, actor})
}

// ResolveCapacityWaiter adjudicates promotion and sealed queue expiry under the captured owner fence.
func (t *Tenant) ResolveCapacityWaiter(ctx context.Context, key string, binding CapacityBinding, actor Actor) (WaiterResolution, error) {
	return t.resolveWaiter(ctx, key, binding.RunID, binding.NodeID, "", "", "", false, &capacityFence{binding, actor})
}

func checkCapacityFenceTx(ctx context.Context, tx *storeTx, team Team, fence capacityFence) error {
	b := fence.binding
	if b.RunID == "" || b.Generation < 0 {
		return holderRefusal()
	}
	if err := checkOwnerTx(ctx, tx, team, fence.actor, b.RunID, tx.now()); err != nil {
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
	if cancelled.Valid || status != string(RunPending) && status != string(RunRunning) {
		return holderRefusal()
	}
	if b.NodeID == "" {
		if b.Generation != fence.actor.Owner.Generation {
			return holderRefusal()
		}
		return nil
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT status,generation,cancel_requested_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), team, b.RunID, b.NodeID).Scan(&status, &generation, &cancelled); err != nil {
		return err
	}
	if status == string(StatusDone) || cancelled.Valid || generation != b.Generation {
		return holderRefusal()
	}
	return nil
}

func checkCapacityMutationTx(ctx context.Context, tx *storeTx, team Team, key, id string, fence *capacityFence) error {
	if fence == nil {
		return nil
	}
	if err := checkCapacityFenceTx(ctx, tx, team, *fence); err != nil {
		return err
	}
	b := fence.binding
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		rows, err := tx.QueryContext(ctx, `SELECT run_id,node_id,generation FROM `+table+` WHERE team=? AND key=? AND ((run_id=? AND node_id=?) OR holder_id=?)`, team, key, b.RunID, b.NodeID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var current CapacityBinding
			if err := rows.Scan(&current.RunID, &current.NodeID, &current.Generation); err != nil {
				return errors.Join(err, rows.Close())
			}
			if current != b {
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
	scope, _, name, ok := runspec.ParseConcurrencyKey(waiter.Key)
	if !ok {
		return time.Time{}, nil
	}
	var groups []runspec.Concurrency
	if waiter.NodeID != "" {
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT execution_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, waiter.RunID, waiter.NodeID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return time.Time{}, nil
			}
			return time.Time{}, err
		}
		var execution runspec.JobExecutionSettings
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &execution); err != nil {
				return time.Time{}, err
			}
		}
		groups = execution.Concurrency
	} else {
		run, err := readRunTx(ctx, tx, team, waiter.RunID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return time.Time{}, nil
			}
			return time.Time{}, err
		}
		if run.Spec != nil {
			groups = run.Spec.Resolved.Settings.Concurrency
		}
	}
	for _, group := range groups {
		if group.Group == name && group.Scope == scope && group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0 {
			return waiter.ArrivedAt.Add(time.Duration(*group.QueueTimeoutMS) * time.Millisecond), nil
		}
	}
	return time.Time{}, nil
}

func cancelSupersededCapacityTx(ctx context.Context, tx *storeTx, team Team, key, id string, actor Actor) error {
	var b CapacityBinding
	err := tx.QueryRowContext(ctx, `SELECT run_id,node_id,generation FROM concurrency_holders WHERE team=? AND key=? AND holder_id=? AND superseded=1`, team, key, id).Scan(&b.RunID, &b.NodeID, &b.Generation)
	if err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, team, b.RunID); err != nil {
		return err
	}
	now := tx.now()
	if b.NodeID != "" {
		row, err := readNodeTx(ctx, tx, team, NodeRef{RunID: b.RunID, NodeID: b.NodeID})
		if err != nil {
			return err
		}
		if row.Generation != b.Generation || row.Status == StatusDone || row.CancelRequestedAt != nil {
			return nil
		}
		row.CancelRequestedAt = &now
		row.CancelReason = "concurrency superseded"
		row.StateSeq++
		row.StateActor = actor
		row.StateDigest, err = stateHash(struct {
			Key, ID string
			Binding CapacityBinding
			Actor   Actor
		}{key, id, b, actor})
		if err != nil {
			return err
		}
		if err := writeNodeStateTx(ctx, tx, team, row); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE runs SET cancel_requested_at=?,cancel_reason='concurrency superseded',state_seq=state_seq+1 WHERE team=? AND id=? AND state_seq=? AND finished_at IS NULL`, now.UnixNano(), team, b.RunID, run.StateSeq)
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
	return appendStateEventTx(ctx, tx, team, NodeRef{RunID: b.RunID}, "run_cancel_requested", run.StateSeq+1, nil, now)
}

func capacityWaiterCurrentTx(ctx context.Context, tx *storeTx, team Team, waiter ConcurrencyWaiter) (bool, error) {
	generation, err := capacityGenerationTx(ctx, tx, team, waiter.RunID, waiter.NodeID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return generation == waiter.generation, err
}

func prepareCapacityAcquisitionTx(ctx context.Context, tx *storeTx, team Team, key string, fence capacityFence) error {
	if err := advisoryLockTx(ctx, tx, fmt.Sprintf("capacity-admission:%d:%s:%s", len(team), team, key)); err != nil {
		return err
	}
	if err := checkOwnerTx(ctx, tx, team, fence.actor, fence.binding.RunID, tx.now()); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT run_id,node_id FROM concurrency_holders WHERE team=? AND key=?
UNION SELECT run_id,node_id FROM concurrency_waiters WHERE team=? AND key=?
UNION SELECT ?,? ORDER BY run_id,node_id`, team, key, team, key, fence.binding.RunID, fence.binding.NodeID)
	if err != nil {
		return err
	}
	var work []NodeRef
	for rows.Next() {
		var ref NodeRef
		if err := rows.Scan(&ref.RunID, &ref.NodeID); err != nil {
			return errors.Join(err, rows.Close())
		}
		work = append(work, ref)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	previous := ""
	for _, ref := range work {
		if ref.RunID == previous {
			continue
		}
		if err := lockStateRunTx(ctx, tx, team, ref.RunID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		previous = ref.RunID
	}
	for _, ref := range work {
		if ref.NodeID == "" {
			continue
		}
		var found string
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), team, ref.RunID, ref.NodeID).Scan(&found); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return checkCapacityFenceTx(ctx, tx, team, fence)
}
