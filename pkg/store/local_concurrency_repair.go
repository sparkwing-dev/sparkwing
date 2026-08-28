package store

import (
	"context"
	"time"
)

const localScopeKeyClause = "(key LIKE 'r:%' OR key LIKE 'b:%')"

const deadLocalPredicate = localScopeKeyClause +
	" AND run_id NOT IN (SELECT id FROM runs WHERE status = ?)"

// PurgeDeadLocalConcurrency deletes local-scope (run and box) concurrency
// holder and waiter rows whose owning run is no longer running, and
// returns how many of each it removed. Global-scope rows -- the cluster's
// fleet locks -- are never touched, so it is safe to run against a machine
// that also drives cluster work.
//
// These rows belong to runs that coordinate through the store rather
// than through the local admission daemon: cluster work, and local runs
// with no daemon available, whose box- and run-scoped groups fall back to
// the store. That fallback has no equivalent of the daemon's instant
// release on socket close, so a killed run leaves its slot held and this
// purge is what frees it -- which makes it live repair, not just cleanup
// after an older admission model.
//
// A run inside the orphan grace window keeps its rows: the predicate asks
// whether the owning run is still marked running, and the doctor's own
// orphan sweep is what flips a crashed one first. Deletes route through
// the canonical row helpers and commit through the invariant check, so
// the repair cannot leave a key in a state the rest of the subsystem
// rejects.
func (s *Store) PurgeDeadLocalConcurrency(ctx context.Context) (holders, waiters int, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	touched := map[string]struct{}{}

	type holderRow struct{ key, holderID string }
	var deadHolders []holderRow
	hrows, err := tx.QueryContext(ctx,
		`SELECT key, holder_id FROM concurrency_holders WHERE `+deadLocalPredicate, runStatusRunning)
	if err != nil {
		return 0, 0, err
	}
	for hrows.Next() {
		var r holderRow
		if err := hrows.Scan(&r.key, &r.holderID); err != nil {
			_ = hrows.Close()
			return 0, 0, err
		}
		deadHolders = append(deadHolders, r)
	}
	if err := hrows.Err(); err != nil {
		_ = hrows.Close()
		return 0, 0, err
	}
	_ = hrows.Close()

	type waiterRow struct{ key, runID, nodeID string }
	var deadWaiters []waiterRow
	wrows, err := tx.QueryContext(ctx,
		`SELECT key, run_id, node_id FROM concurrency_waiters WHERE `+deadLocalPredicate, runStatusRunning)
	if err != nil {
		return 0, 0, err
	}
	for wrows.Next() {
		var r waiterRow
		if err := wrows.Scan(&r.key, &r.runID, &r.nodeID); err != nil {
			_ = wrows.Close()
			return 0, 0, err
		}
		deadWaiters = append(deadWaiters, r)
	}
	if err := wrows.Err(); err != nil {
		_ = wrows.Close()
		return 0, 0, err
	}
	_ = wrows.Close()

	for _, r := range deadHolders {
		if err := txDeleteHolder(ctx, tx, r.key, r.holderID); err != nil {
			return 0, 0, err
		}
		touched[r.key] = struct{}{}
	}
	for _, r := range deadWaiters {
		if _, err := txDeleteWaiter(ctx, tx, r.key, r.runID, r.nodeID); err != nil {
			return 0, 0, err
		}
		touched[r.key] = struct{}{}
	}

	keys := make([]string, 0, len(touched))
	for k := range touched {
		keys = append(keys, k)
	}
	if err := txCommitChecked(ctx, tx, time.Now().UnixNano(), keys...); err != nil {
		return 0, 0, err
	}
	return len(deadHolders), len(deadWaiters), nil
}

// CountDeadLocalConcurrency reports how many local-scope holder and
// waiter rows [PurgeDeadLocalConcurrency] would remove, without removing
// them. It backs the dry-run path of the repair.
func (s *Store) CountDeadLocalConcurrency(ctx context.Context) (holders, waiters int, err error) {
	countDead := func(table string) (int, error) {
		row := s.queryRow(ctx,
			`SELECT COUNT(*) FROM `+table+` WHERE `+deadLocalPredicate, runStatusRunning)
		var n int
		if err := row.Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	if holders, err = countDead("concurrency_holders"); err != nil {
		return 0, 0, err
	}
	if waiters, err = countDead("concurrency_waiters"); err != nil {
		return 0, 0, err
	}
	return holders, waiters, nil
}
