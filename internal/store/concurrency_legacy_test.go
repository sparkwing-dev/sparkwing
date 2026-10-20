package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AcquireConcurrencySlot atomically admits work using this tenant's capacity, cache and waiters.
func (t *Tenant) AcquireConcurrencySlot(ctx context.Context, req AcquireSlotRequest) (AcquireSlotResponse, error) {
	return t.acquireConcurrencySlot(ctx, req, nil)
}

// ResolveWaiter reads this tenant's waiter or cached result without inserting a waiter.
func (t *Tenant) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (WaiterResolution, error) {
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

	var hasEntry int
	err = tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), key,
	).Scan(&hasEntry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key})
	}
	if err != nil {
		return nil, err
	}

	now := tx.now()
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
	now := tx.now()
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
	if err := txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key}); err != nil {
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
	var hasEntry int
	err = tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM concurrency_entries WHERE team = ? AND key = ?`+t.s.forUpdate(), string(t.team), key,
	).Scan(&hasEntry)
	entryDeclared := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil, err
	}

	released, _, _, err = txReleaseHolder(ctx, tx, t.team, key, holderID)
	if err != nil {
		return false, nil, nil, err
	}

	if !entryDeclared {
		return released, followers, nil, txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key})
	}
	now := tx.now()
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
	if err := txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key}); err != nil {
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
		now := tx.now()
		if _, err := txPromoteWaiters(ctx, tx, t.team, key, now.UnixNano(), now.Add(defaultConcurrencyLease).UnixNano()); err != nil {
			return false, err
		}
	}
	if err := txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key}); err != nil {
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
