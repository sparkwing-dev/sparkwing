package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Fixture adapters expose the shared accounting primitives to external-package tests.
type (
	AcquireSlotRequest = acquireSlotRequest
	WaiterResolution   = waiterResolution
	WaiterStatus       = waiterStatus
)

const (
	WaiterStillWaiting   = waiterStillWaiting
	WaiterLeaderFinished = waiterLeaderFinished
)

// AcquireConcurrencySlot atomically admits work using this tenant's capacity, cache and waiters.
func (t *Tenant) AcquireConcurrencySlot(ctx context.Context, req acquireSlotRequest) (AcquireSlotResponse, error) {
	return t.acquireConcurrencySlot(ctx, req, nil)
}

// ResolveWaiter reads this tenant's waiter or cached result without inserting a waiter.
func (t *Tenant) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (waiterResolution, error) {
	return t.resolveWaiter(ctx, key, runID, nodeID, leaderRunID, leaderNodeID, nil)
}

// CancelWaiter removes this tenant's waiter or promoted holder for the named work.
func (t *Tenant) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return t.cancelWaiter(ctx, key, runID, nodeID, nil)
}

// PromoteNextWaiters admits this tenant's queued waiters in arrival order.
func (t *Tenant) PromoteNextWaiters(ctx context.Context, key string, lease time.Duration) ([]ConcurrencyWaiter, error) {
	if lease <= 0 {
		lease = defaultConcurrencyLease
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return nil, err
	}

	now := tx.now(ctx)
	nowNS := now.UnixNano()
	expiresNS := now.Add(lease).UnixNano()
	promote, err := txPromoteWaiters(ctx, tx, t.team, key, nowNS, expiresNS)
	if err != nil {
		return nil, err
	}
	if err := txCommitChecked(ctx, tx, nowNS, concurrencyKey{t.team, key}); err != nil {
		return nil, err
	}
	return promote, nil
}

// HeartbeatConcurrencySlot renews an unexpired tenant holder and reports supersession.
func (t *Tenant) HeartbeatConcurrencySlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	if lease <= 0 {
		lease = defaultConcurrencyLease
	}
	return t.heartbeatConcurrencySlotOnce(ctx, key, holderID, lease)
}

func (t *Tenant) heartbeatConcurrencySlot(ctx context.Context, key, holderID string, lease time.Duration, sleep func(time.Duration)) (expires time.Time, superseded bool, err error) {
	if lease <= 0 {
		lease = defaultConcurrencyLease
	}
	err = retryOnBusyWithSleep(func() error {
		var once error
		expires, superseded, once = t.heartbeatConcurrencySlotOnce(ctx, key, holderID, lease)
		return once
	}, sleep)
	return expires, superseded, err
}

func (t *Tenant) heartbeatConcurrencySlotOnce(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock first (same order as acquire) so a lease
	// extension can't race a promotion admitting into the same budget.
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return time.Time{}, false, err
	}

	// safety: clock read after the lock, or a stale timestamp lets a
	// heartbeat revive a holder whose lease already lapsed.
	now := tx.now(ctx)
	expires = now.Add(lease)

	var superInt int
	var leaseNS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT superseded, lease_expires_at FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`,
		string(t.team), key, holderID,
	).Scan(&superInt, &leaseNS)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, ErrLockHeld
	}
	if err != nil {
		return time.Time{}, false, err
	}
	// safety: expired lease means admission may have reassigned the budget; reviving would double-admit.
	if leaseNS <= now.UnixNano() {
		return time.Time{}, false, ErrLockHeld
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE team = ? AND key = ? AND holder_id = ?`,
		expires.UnixNano(), string(t.team), key, holderID,
	); err != nil {
		return time.Time{}, false, err
	}
	if err := txCommitChecked(ctx, tx, now.UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return time.Time{}, false, err
	}
	return expires, superInt == 1, nil
}

// ReleaseConcurrencySlot releases a tenant holder and stores its reusable cache result.
func (t *Tenant) ReleaseConcurrencySlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) (bool, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock first, keeping the one lock order shared
	// with acquire and release+promote.
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return false, err
	}

	released, _, _, err := txReleaseHolder(ctx, tx, t.team, key, holderID)
	if err != nil {
		return false, err
	}
	if err := txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return false, err
	}
	return released, nil
}

// ReleaseAndNotify atomically releases a tenant holder, resolves followers and promotes waiters.
func (t *Tenant) ReleaseAndNotify(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl, promoteLease time.Duration) (released bool, followers, promoted []ConcurrencyWaiter, err error) {
	if promoteLease <= 0 {
		promoteLease = defaultConcurrencyLease
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock before any row writes (same order as
	// acquire) so release+promote serializes with admissions without
	// inverting lock order against a concurrent acquire.
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return false, nil, nil, err
	}

	released, _, _, err = txReleaseHolder(ctx, tx, t.team, key, holderID)
	if err != nil {
		return false, nil, nil, err
	}

	now := tx.now(ctx)
	nowNS := now.UnixNano()
	expiresNS := now.Add(promoteLease).UnixNano()
	promoted, err = txPromoteWaiters(ctx, tx, t.team, key, nowNS, expiresNS)
	if err != nil {
		return false, nil, nil, err
	}

	if err := txCommitChecked(ctx, tx, nowNS, concurrencyKey{t.team, key}); err != nil {
		return false, nil, nil, err
	}
	return released, followers, promoted, nil
}

// ForceReleaseSupersededHolders removes this tenant's superseded holders.
func (t *Tenant) ForceReleaseSupersededHolders(ctx context.Context, key string) ([]ConcurrencyHolder, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: entries-row lock first; dropping superseded rows changes
	// what a concurrent promotion may reclaim.
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders WHERE team = ? AND key = ? AND superseded = 1`, string(t.team), key,
	)
	if err != nil {
		return nil, err
	}
	var out []ConcurrencyHolder
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, h)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range out {
		if err := txDeleteHolder(ctx, tx, h.team, h.Key, h.HolderID); err != nil {
			return nil, err
		}
	}
	if err := txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return nil, err
	}
	return out, nil
}

func (t *Tenant) cancelWaiter(ctx context.Context, key, runID, nodeID string, fence *capacityFence) (bool, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, key, *fence); err != nil {
			return false, err
		}
	}

	// safety: entries-row lock first; the cancel may reclaim a holder and
	// promote, which must serialize with concurrent admissions.
	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return false, err
	}

	if err := checkCapacityMutationTx(ctx, tx, t.team, key, "", fence); err != nil {
		return false, err
	}

	waiterDeleted, err := txDeleteWaiter(ctx, tx, t.team, key, runID, nodeID)
	if err != nil {
		return false, err
	}

	hres, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`,
		string(t.team), key, runID, nodeID,
	)
	if err != nil {
		return false, err
	}
	hn, err := hres.RowsAffected()
	if err != nil {
		return false, err
	}
	if hn > 0 {
		now := tx.now(ctx)
		if _, err := txPromoteWaiters(ctx, tx, t.team, key, now.UnixNano(), now.Add(defaultConcurrencyLease).UnixNano()); err != nil {
			return false, err
		}
	}
	if err := txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return false, err
	}
	return waiterDeleted || hn > 0, nil
}

// ConcurrencyHolder reads a legacy fixture holder by tenant.
func (t *Tenant) ConcurrencyHolder(ctx context.Context, key, holderID string, now time.Time) (*ConcurrencyHolder, error) {
	return t.concurrencyHolder(ctx, key, holderID, now, holderLeaseLiveSQL(""))
}

func (t *Tenant) concurrencyHolder(ctx context.Context, key, holderID string, now time.Time, livePredicate string) (*ConcurrencyHolder, error) {
	row := t.s.queryRow(ctx,
		`SELECT `+holderColumns+`
		   FROM concurrency_holders
		  WHERE team = ?
		    AND key = ?
		    AND holder_id = ?
		    AND `+livePredicate,
		string(t.team), key, holderID, now.UnixNano(),
	)
	holder, err := scanHolder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("concurrency holder", key+"/"+holderID)
	}
	if err != nil {
		return nil, err
	}
	return &holder, nil
}

func holderLeaseLiveSQL(alias string) string {
	return alias + "lease_expires_at > ?"
}

func (t *Tenant) acquireConcurrencySlot(ctx context.Context, req acquireSlotRequest, fence *capacityFence) (AcquireSlotResponse, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	defer rollbackOrLog(tx)
	result, err := t.acquireConcurrencySlotTx(ctx, tx, req, fence, time.Time{})
	if err != nil {
		return AcquireSlotResponse{}, err
	}
	if err := txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, req.Key}); err != nil {
		return AcquireSlotResponse{}, err
	}
	return result, nil
}

func (t *Tenant) resolveWaiter(ctx context.Context, key, runID, nodeID, leaderRunID, leaderNodeID string, fence *capacityFence) (waiterResolution, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return waiterResolution{}, err
	}
	defer rollbackOrLog(tx)
	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, key, *fence); err != nil {
			return waiterResolution{}, err
		}
	}
	result, err := t.resolveWaiterTx(ctx, tx, key, runID, nodeID, leaderRunID, leaderNodeID, defaultConcurrencyLease, fence)
	if err != nil {
		return waiterResolution{}, err
	}
	if err := txCommitChecked(ctx, tx, tx.now(ctx).UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return waiterResolution{}, err
	}
	return result, nil
}

func txCommitChecked(ctx context.Context, tx *storeTx, nowNS int64, keys ...concurrencyKey) error {
	seen := make(map[concurrencyKey]bool, len(keys))
	for _, k := range keys {
		if k.key == "" || seen[k] {
			continue
		}
		seen[k] = true
		if err := txCheckConcurrencyInvariants(ctx, tx, k.team, k.key, nowNS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// safety: leader reads retain tenant scope before disclosing an outcome.
func txNodeOutcome(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) (outcome, failureReason string, found bool, err error) {
	err = tx.QueryRowContext(
		ctx,
		`SELECT outcome, failure_reason FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID,
	).Scan(&outcome, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return outcome, failureReason, true, nil
}

// waiterStatus is what a polling waiter observes.
type waiterStatus string

const (
	waiterStillWaiting waiterStatus = "still_waiting"
	waiterPromoted     waiterStatus = "promoted"

	waiterLeaderFinished waiterStatus = "leader_finished"

	waiterCancelled waiterStatus = "cancelled"
	waiterExpired   waiterStatus = "expired"
)

// waiterResolution: fields populate per Status.
type waiterResolution struct {
	Status              waiterStatus
	HolderID            string
	HolderLeaseExpires  time.Time
	OutputRef           string
	OriginRunID         string
	OriginNodeID        string
	LeaderRunID         string
	LeaderNodeID        string
	LeaderOutcome       string
	LeaderFailureReason string

	Position    int
	QueueLength int
	Holders     []ConcurrencyHolder
}

func (t *Tenant) resolveWaiterTx(ctx context.Context, tx *storeTx, key, runID, nodeID, leaderRunID, leaderNodeID string, lease time.Duration, fence *capacityFence) (waiterResolution, error) {
	var err error

	if fence != nil {
		if err := checkCapacityFenceTx(ctx, tx, t.team, key, *fence); err != nil {
			return waiterResolution{}, err
		}
	}

	if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
		return waiterResolution{}, err
	}
	if err := checkCapacityMutationTx(ctx, tx, t.team, key, "", fence); err != nil {
		return waiterResolution{}, err
	}
	nowNS := tx.now(ctx).UnixNano()
	if _, err := reapStaleConcurrencyHoldersTx(ctx, tx, t.team, key); err != nil {
		return waiterResolution{}, err
	}

	var holderID string
	var leaseNS int64
	var superInt int
	err = tx.QueryRowContext(
		ctx,
		`SELECT holder_id, lease_expires_at, superseded
		   FROM concurrency_holders
		  WHERE team = ? AND key = ? AND run_id = ? AND node_id = ?`,
		string(t.team), key, runID, nodeID,
	).Scan(&holderID, &leaseNS, &superInt)
	switch {
	case err == nil && superInt == 0 && leaseNS > nowNS:
		return waiterResolution{
			Status:             waiterPromoted,
			HolderID:           holderID,
			HolderLeaseExpires: time.Unix(0, leaseNS),
		}, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return waiterResolution{}, err
	}

	waiter, err := scanWaiter(tx.QueryRowContext(
		ctx,
		`SELECT `+waiterColumns+` FROM concurrency_waiters WHERE team = ? AND key = ? AND run_id = ? AND node_id = ? AND lease_expires_at > ?`,
		string(t.team), key, runID, nodeID, nowNS,
	))
	if err == nil {
		deadline, err := capacityQueueDeadlineTx(ctx, tx, t.team, waiter)
		if err != nil {
			return waiterResolution{}, err
		}
		if !deadline.IsZero() && !deadline.After(tx.now(ctx)) {
			if _, err := txDeleteWaiter(ctx, tx, t.team, key, runID, nodeID); err != nil {
				return waiterResolution{}, err
			}
			return waiterResolution{Status: waiterExpired}, nil
		}

		shouldPromote := waiter.Policy == OnLimitQueue
		if waiter.Policy == OnLimitCancelOthers {
			var holderCount int
			if err := tx.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM concurrency_holders WHERE team = ? AND key = ?`,
				string(t.team), key,
			).Scan(&holderCount); err != nil {
				return waiterResolution{}, err
			}
			shouldPromote = holderCount == 0
		}
		if shouldPromote {
			now := tx.now(ctx)
			promoted, err := txPromoteWaiters(ctx, tx, t.team, key, now.UnixNano(), now.Add(lease).UnixNano())
			if err != nil {
				return waiterResolution{}, err
			}
			for _, waiter := range promoted {
				if waiter.RunID == runID && waiter.NodeID == nodeID {
					return waiterResolution{
						Status:             waiterPromoted,
						HolderID:           waiter.HolderID,
						HolderLeaseExpires: now.Add(lease),
					}, nil
				}
			}
		}
		var position int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?) AND arrived_at < ?`,
			string(t.team), key, OnLimitQueue, OnLimitCancelOthers, waiter.ArrivedAt.UnixNano(),
		).Scan(&position); e != nil {
			return waiterResolution{}, e
		}
		var queueLength int
		if e := tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM concurrency_waiters WHERE team = ? AND key = ? AND policy IN (?, ?)`,
			string(t.team), key, OnLimitQueue, OnLimitCancelOthers,
		).Scan(&queueLength); e != nil {
			return waiterResolution{}, e
		}
		holders, e := txActiveHolders(ctx, tx, t.team, key, nowNS)
		if e != nil {
			return waiterResolution{}, e
		}
		return waiterResolution{Status: waiterStillWaiting, Position: position, QueueLength: queueLength, Holders: holders}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return waiterResolution{}, err
	}

	if leaderRunID != "" {
		leaderOutcome, leaderReason, found, err := txNodeOutcome(ctx, tx, t.team, leaderRunID, leaderNodeID)
		if err != nil {
			return waiterResolution{}, err
		}
		if !found || leaderOutcome != "success" {
			return waiterResolution{Status: waiterCancelled}, nil
		}
		return waiterResolution{
			Status:              waiterLeaderFinished,
			LeaderRunID:         leaderRunID,
			LeaderNodeID:        leaderNodeID,
			LeaderOutcome:       leaderOutcome,
			LeaderFailureReason: leaderReason,
		}, nil
	}

	return waiterResolution{Status: waiterCancelled}, nil
}

func txPromoteWaiters(ctx context.Context, tx *storeTx, team Team, key string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	if _, err := reapStaleConcurrencyHoldersTx(ctx, tx, team, key); err != nil {
		return nil, err
	}
	return txPromoteWaitersLocked(ctx, tx, team, key, nowNS, expiresNS)
}

func txPromoteWaitersLocked(ctx context.Context, tx *storeTx, team Team, key string, nowNS, expiresNS int64) ([]ConcurrencyWaiter, error) {
	acct, err := txConcurrencyAccounting(ctx, tx, team, key, nowNS)
	if err != nil {
		return nil, err
	}
	entryCap, holderMin, used := acct.entryCap, acct.floor, acct.used

	rows, err := tx.QueryContext(
		ctx,
		`SELECT `+waiterColumns+`
		   FROM concurrency_waiters
		  WHERE team = ? AND key = ? AND policy IN (?, ?) AND lease_expires_at > ?
		  ORDER BY arrived_at ASC`,
		string(team), key, OnLimitQueue, OnLimitCancelOthers, nowNS,
	)
	if err != nil {
		return nil, err
	}
	var candidates []ConcurrencyWaiter
	for rows.Next() {
		w, serr := scanWaiter(rows)
		if serr != nil {
			_ = rows.Close()
			return nil, serr
		}
		candidates = append(candidates, w)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var promoted []ConcurrencyWaiter
	for _, w := range candidates {
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admission_work WHERE team=? AND run_id=? AND node_id=?)`, team, w.RunID, w.NodeID).Scan(&owned); err != nil {
			return nil, err
		}
		if owned {
			continue
		}
		current, err := capacityWaiterCurrentTx(ctx, tx, team, w)
		if err != nil {
			return nil, err
		}
		if !current {
			continue
		}
		deadline, err := capacityQueueDeadlineTx(ctx, tx, team, w)
		if err != nil {
			return nil, err
		}
		if !deadline.IsZero() && !deadline.After(tx.now(ctx)) {
			continue
		}

		c, candCap := waiterBudget(w, entryCap, holderMin)
		if !fitsBudget(used, c, candCap) {
			if blockedByYoungerHolders(acct.holders, w.ArrivedAt.UnixNano(), c, candCap) {
				break
			}
			continue
		}
		used += c
		if holderMin == 0 || candCap < holderMin {
			holderMin = candCap
		}
		promoted = append(promoted, w)
	}

	for i, w := range promoted {
		newHolder := w.HolderID
		if newHolder == "" {
			newHolder = fmt.Sprintf("%s/%s", w.RunID, nodeIDOrDash(w.NodeID))
		}
		if _, err := txDeleteWaiter(ctx, tx, w.team, w.Key, w.RunID, w.NodeID); err != nil {
			return nil, err
		}
		c := w.Cost
		if c <= 0 {
			c = 1
		}
		dc := w.DeclaredCapacity
		if dc <= 0 {
			dc = entryCap
		}
		if err := txInsertHolder(ctx, tx, holderRow{
			team: w.team, key: w.Key, holderID: newHolder, runID: w.RunID, nodeID: w.NodeID,
			cost: c, declaredCapacity: dc, queueArrivedNS: w.ArrivedAt.UnixNano(),
		}, nowNS, expiresNS); err != nil {
			return nil, err
		}
		promoted[i].HolderID = newHolder
	}
	return promoted, nil
}

func txReleaseHolder(ctx context.Context, tx *storeTx, team Team, key, holderID string) (released bool, runID, nodeID string, err error) {
	var queueArrivedNS int64
	err = tx.QueryRowContext(
		ctx,
		`SELECT run_id, node_id, queue_arrived_at
		   FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`,
		string(team), key, holderID,
	).Scan(&runID, &nodeID, &queueArrivedNS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}

	now := tx.now(ctx)

	if nodeID == "" && queueArrivedNS > 0 {
		if err := txSupersede(ctx, tx, team, key, holderID); err != nil {
			return false, "", "", err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE concurrency_holders
   SET lease_expires_at = ?, cost = 0
 WHERE team = ? AND key = ? AND holder_id = ?`, now.UnixNano(), string(team), key, holderID); err != nil {
			return false, "", "", err
		}
	} else {
		if err := txDeleteHolder(ctx, tx, team, key, holderID); err != nil {
			return false, "", "", err
		}
	}

	return true, runID, nodeID, nil
}

func txDeleteHolder(ctx context.Context, tx *storeTx, team Team, key, holderID string) error {
	_, err := tx.ExecContext(
		ctx,
		`DELETE FROM concurrency_holders WHERE team = ? AND key = ? AND holder_id = ?`,
		string(team), key, holderID,
	)
	return err
}
