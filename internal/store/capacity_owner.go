package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type capacityFence struct {
	binding CapacityBinding
	actor   Actor
	source  *CapacityBinding
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
	if cancelled.Valid || status != string(RunPending) && status != string(RunRunning) {
		return holderRefusal()
	}
	if b.NodeID == "" {
		generation, err := capacityGenerationTx(ctx, tx, team, b.RunID, "")
		if err != nil {
			return err
		}
		if b.Generation != generation {
			return holderRefusal()
		}
	} else {
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT status,generation,cancel_requested_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`+tx.forUpdate(), team, b.RunID, b.NodeID).Scan(&status, &generation, &cancelled); err != nil {
			return err
		}
		evaluation, err := capacityEvaluationTx(ctx, tx, team, b, fence.actor)
		if err != nil {
			return err
		}
		if status == string(StatusDone) && !evaluation || cancelled.Valid || generation != b.Generation {
			return holderRefusal()
		}
	}
	return nil
}

func capacityEvaluationTx(ctx context.Context, tx *storeTx, team Team, binding CapacityBinding, actor Actor) (bool, error) {
	if actor.Authority.Kind != "" {
		principal, err := tx.ResolveAuthority(ctx, actor.Authority)
		if err != nil {
			return false, err
		}
		if principal.Capability.Unit == capability.UnitEval {
			return true, nil
		}
	}
	if binding.NodeID != "" && actor.Kind == ActorRunHost {
		row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: binding.RunID, NodeID: binding.NodeID})
		if err != nil {
			return false, err
		}
		return row.Status == StatusDone && row.Expansion == ExpansionExpected, nil
	}
	return false, nil
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
		row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: b.RunID, NodeID: b.NodeID})
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
	return appendStateEventTx(ctx, tx, team, controllerwire.NodeRef{RunID: b.RunID}, "run_cancel_requested", run.StateSeq+1, nil, now)
}

func capacityWaiterCurrentTx(ctx context.Context, tx *storeTx, team Team, waiter ConcurrencyWaiter) (bool, error) {
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT lease_expires_at FROM concurrency_waiters WHERE team=? AND key=? AND run_id=? AND node_id=?`, team, waiter.Key, waiter.RunID, waiter.NodeID).Scan(&expires); err != nil {
		return false, err
	}
	return capacityContinuousTx(ctx, tx, team, CapacityBinding{RunID: waiter.RunID, NodeID: waiter.NodeID, Generation: waiter.generation}, expires)
}

func capacityContinuousTx(ctx context.Context, tx *storeTx, team Team, binding CapacityBinding, expires int64) (bool, error) {
	generation, err := capacityGenerationTx(ctx, tx, team, binding.RunID, binding.NodeID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A node generation replaces execution; an owner generation only transfers reconciliation.
	if binding.NodeID != "" && generation != binding.Generation {
		return false, nil
	}
	ceiling, err := capacityLeaseCeilingTx(ctx, tx, team, binding.RunID, binding.NodeID, expires)
	return ceiling > tx.now().UnixNano(), err
}

func prepareCapacityAcquisitionTx(ctx context.Context, tx *storeTx, team Team, keys []string, fence capacityFence) error {
	for _, key := range keys {
		name := fmt.Sprintf("capacity-admission:%d:%s:%s", len(team), team, key)
		// safety: settlement already owns its run row, so it must never wait behind admission that needs that row.
		if fence.actor.Kind == ActorController && tx.dialect == DialectPostgres {
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
	if err := lockClaimControllerTx(ctx, tx); err != nil {
		return err
	}
	if fence.actor.Authority.Kind != "" {
		if _, err := authorityChainTx(ctx, tx, fence.actor.Authority, true); err != nil {
			return err
		}
	}
	if err := checkCapacityOwnerTx(ctx, tx, team, fence); err != nil {
		return err
	}
	if err := lockCapacityWorkTx(ctx, tx, team, keys, controllerwire.NodeRef{RunID: fence.binding.RunID, NodeID: fence.binding.NodeID}, fence.actor.Kind == ActorController); err != nil {
		return err
	}
	return checkCapacityUnitTx(ctx, tx, team, fence)
}

func lockCapacityWorkTx(ctx context.Context, tx *storeTx, team Team, keys []string, ref controllerwire.NodeRef, nonblocking bool) error {
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
	rows, err := tx.QueryContext(ctx, `SELECT run_id,node_id FROM concurrency_holders WHERE team=? AND key IN (`+predicate+`)
UNION SELECT run_id,node_id FROM concurrency_waiters WHERE team=? AND key IN (`+predicate+`)
UNION SELECT ?,? ORDER BY run_id,node_id`, args...)
	if err != nil {
		return err
	}
	var work []controllerwire.NodeRef
	for rows.Next() {
		var ref controllerwire.NodeRef
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
		if nonblocking && tx.dialect == DialectPostgres {
			var found string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team=? AND id=?`+tx.forNoKeyUpdate()+` SKIP LOCKED`, team, ref.RunID).Scan(&found); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					var exists bool
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE team=? AND id=?)`, team, ref.RunID).Scan(&exists); err != nil {
						return err
					}
					if exists {
						return errCapacityAdmissionBusy
					}
					continue
				}
				return err
			}
		} else if err := lockStateRunTx(ctx, tx, team, ref.RunID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		previous = ref.RunID
	}
	for _, ref := range work {
		if ref.NodeID == "" {
			continue
		}
		var found string
		lock := tx.forUpdate()
		if nonblocking && tx.dialect == DialectPostgres {
			lock += " SKIP LOCKED"
		}
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM nodes WHERE team=? AND run_id=? AND node_id=?`+lock, team, ref.RunID, ref.NodeID).Scan(&found); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if nonblocking && tx.dialect == DialectPostgres {
					var exists bool
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE team=? AND run_id=? AND node_id=?)`, team, ref.RunID, ref.NodeID).Scan(&exists); err != nil {
						return err
					}
					if exists {
						return errCapacityAdmissionBusy
					}
				}
			} else {
				return err
			}
		}
	}
	return nil
}

func checkCapacityOwnerTx(ctx context.Context, tx *storeTx, team Team, fence capacityFence) error {
	binding := fence.binding
	if fence.source != nil {
		binding = *fence.source
	}
	if fence.actor.Authority.Kind != "" {
		principal, err := tx.ResolveAuthority(ctx, fence.actor.Authority)
		if err != nil {
			return err
		}
		if principal.Capability.Unit == capability.UnitEval {
			if binding.NodeID == "" {
				return holderRefusal()
			}
			return expansionAuthorityTx(ctx, tx, team, ExpansionSubmission{Source: controllerwire.NodeRef{RunID: binding.RunID, NodeID: binding.NodeID}, SourceGeneration: binding.Generation, Actor: fence.actor}, tx.now())
		}
	}
	if fence.actor.Kind == ActorHolder {
		if binding.NodeID == "" || binding.Generation != fence.actor.Generation {
			return holderRefusal()
		}
		row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: binding.RunID, NodeID: binding.NodeID})
		if err != nil {
			return err
		}
		return checkHolderTx(ctx, tx, team, row, fence.actor, tx.now())
	}
	check := checkOwnerTx
	if binding.NodeID == "" {
		check = checkOwnerLeaseTx
	}
	return check(ctx, tx, team, fence.actor, binding.RunID, tx.now())
}
