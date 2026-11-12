package store

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func groupWaitExpired(row runstate.NodeRow, now time.Time) bool {
	if row.GroupWaitStartedAt == nil {
		return false
	}
	for _, group := range row.Execution.Concurrency {
		if group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0 && !row.GroupWaitStartedAt.Add(time.Duration(*group.QueueTimeoutMS)*time.Millisecond).After(now) {
			return true
		}
	}
	return false
}

func recordGroupWaitTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, observed groupWaitObservation) error {
	row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
	if err != nil {
		return err
	}
	// safety: an award or retry can replace the queue after the read snapshot.
	if !observed.at.IsZero() && (row.Generation != observed.generation || groupWaitQueue(row) != observed.queue) {
		return nil
	}
	timed := false
	for _, group := range row.Execution.Concurrency {
		timed = timed || group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0
	}
	if !timed {
		return nil
	}
	now := tx.now(ctx)
	start := groupWaitStart(now, observed.at)
	if row.GroupWaitStartedAt == nil || !observed.at.IsZero() && start.Before(*row.GroupWaitStartedAt) {
		_, err = tx.execContext(ctx, `UPDATE nodes SET group_wait_started_at=? WHERE team=? AND run_id=? AND node_id=? AND status='pending' AND claimed_by IS NULL`, start.UnixNano(), team, unit.RunID, unit.NodeID)
		if err != nil {
			return err
		}
		row.GroupWaitStartedAt = &start
	}
	if !groupWaitExpired(row, now) {
		return nil
	}
	run, err := readRunTx(ctx, tx, team, string(unit.RunID))
	if err != nil {
		return err
	}
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	_, err = reapUnclaimedNodeTx(ctx, tx, expiredUnit{team: team, ref: row.Ref}, actor, row, run, now)
	return err
}

func groupWaitStart(now, observed time.Time) time.Time {
	if observed.IsZero() || observed.After(now) {
		return now
	}
	return observed
}

func noteGroupWait(tx *storeTx, unit UnitRef, row runstate.NodeRow, now time.Time) {
	if row.GroupWaitStartedAt != nil && !groupWaitExpired(row, now) {
		return
	}
	if tx.groupWaits == nil {
		return
	}
	for _, group := range row.Execution.Concurrency {
		if group.QueueTimeoutMS != nil && *group.QueueTimeoutMS > 0 {
			if _, found := tx.groupWaits[unit]; !found {
				tx.groupWaits[unit] = groupWaitObservation{at: now, generation: row.Generation, queue: groupWaitQueue(row)}
			}
			return
		}
	}
}

func (o *Operator) recordObservedGroupWait(ctx context.Context, demand UnitDemand, unit UnitRef, observed groupWaitObservation) error {
	return o.s.inBlobTx(ctx, func(tx *storeTx) error {
		if err := lockClaimQueueTx(ctx, tx); err != nil {
			return err
		}
		if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}}); err != nil {
			return err
		}
		prepared, err := prepareUnitClaimTx(ctx, tx, demand, claimHead{Unit: unit})
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return recordGroupWaitTx(ctx, tx, prepared.team, unit, observed)
	})
}

type groupWaitObservation struct {
	at         time.Time
	generation runspec.ClaimGeneration
	queue      int64
}

func groupWaitQueue(row runstate.NodeRow) int64 {
	if row.QueueStartedAt != nil {
		return row.QueueStartedAt.UnixNano()
	}
	if row.ReadyAt != nil {
		return row.ReadyAt.UnixNano()
	}
	return 0
}
