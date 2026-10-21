package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

const capacityRunLiveSQL = `r.status IN ('pending','running') AND r.finished_at IS NULL AND (
 ((` + controllerRunOwnership + `) AND EXISTS (
  SELECT 1 FROM leases l WHERE l.name='controller.owner' AND l.holder<>'' AND l.until>?))
 OR ((NOT (` + controllerRunOwnership + `)) AND EXISTS (
  SELECT 1 FROM triggers t WHERE t.team=r.team AND t.id=r.id AND t.status='claimed' AND t.lease_expires_at>?))
 OR EXISTS (SELECT 1 FROM nodes n WHERE n.team=r.team AND n.run_id=r.id
  AND n.status<>'done' AND n.claimed_by IS NOT NULL AND n.lease_expires_at>?))`

func capacityGenerationTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) (int64, error) {
	var generation int64
	var err error
	if nodeID != "" {
		err = tx.QueryRowContext(ctx, `SELECT generation FROM nodes WHERE team=? AND run_id=? AND node_id=? AND status<>'done'`, team, runID, nodeID).Scan(&generation)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN `+controllerRunOwnership+`
 THEN (SELECT generation FROM leases WHERE name='controller.owner')
 ELSE (SELECT generation FROM triggers WHERE team=r.team AND id=r.id) END
 FROM runs r WHERE r.team=? AND r.id=? AND r.status IN ('pending','running')`, team, runID).Scan(&generation)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return generation, err
}

func lockBoundCapacityTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, includePlan bool) error {
	plan := 0
	if includePlan {
		plan = 1
	}
	rows, err := tx.QueryContext(ctx, `SELECT key FROM concurrency_holders WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ? OR (? = 1 AND node_id = ''))
UNION SELECT key FROM concurrency_waiters WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ? OR (? = 1 AND node_id = '')) ORDER BY key`, team, runID, nodeID, nodeID, plan, team, runID, nodeID, nodeID, plan)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return errors.Join(err, rows.Close())
		}
		keys = append(keys, key)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, key := range keys {
		if err := lockCapacityEntryTx(ctx, tx, team, key); err != nil {
			return err
		}
	}
	return nil
}

// safety: callers hold the renewed owner or node fence; an elapsed capacity lease is never revived.
func renewCapacityTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, now, expires time.Time) error {
	if err := lockBoundCapacityTx(ctx, tx, team, runID, nodeID, true); err != nil {
		return err
	}
	now = tx.time
	if !expires.After(now) {
		return holderRefusal()
	}
	_, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at = CASE WHEN lease_expires_at < ? THEN ? ELSE lease_expires_at END
WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ? OR node_id = '') AND lease_expires_at > ? AND superseded = 0
AND EXISTS (SELECT 1 FROM runs r WHERE r.team = concurrency_holders.team AND r.id = concurrency_holders.run_id AND r.finished_at IS NULL)
AND (node_id = '' OR EXISTS (SELECT 1 FROM nodes n WHERE n.team = concurrency_holders.team AND n.run_id = concurrency_holders.run_id AND n.node_id = concurrency_holders.node_id AND n.status <> 'done' AND n.cancel_requested_at IS NULL))`, expires.UnixNano(), expires.UnixNano(), team, runID, nodeID, nodeID, now.UnixNano())
	if err != nil || nodeID != "" {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE concurrency_waiters SET lease_expires_at = CASE WHEN lease_expires_at < ? THEN ? ELSE lease_expires_at END
WHERE team = ? AND run_id = ? AND lease_expires_at > ?
AND EXISTS (SELECT 1 FROM runs r WHERE r.team = concurrency_waiters.team AND r.id = concurrency_waiters.run_id AND r.finished_at IS NULL)
AND (node_id = '' OR EXISTS (SELECT 1 FROM nodes n WHERE n.team = concurrency_waiters.team AND n.run_id = concurrency_waiters.run_id AND n.node_id = concurrency_waiters.node_id AND n.status <> 'done' AND n.cancel_requested_at IS NULL))`, expires.UnixNano(), expires.UnixNano(), team, runID, now.UnixNano())
	return err
}

func releaseCapacityTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) error {
	if err := lockBoundCapacityTx(ctx, tx, team, runID, nodeID, false); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM concurrency_holders WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ?)`, team, runID, nodeID, nodeID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM concurrency_waiters WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ?)`, team, runID, nodeID, nodeID)
	return err
}

func rebindCapacityGenerationTx(ctx context.Context, tx *storeTx, team Team, ref controllerwire.NodeRef, previous, generation int64) error {
	if err := lockBoundCapacityTx(ctx, tx, team, ref.RunID, ref.NodeID, false); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET generation=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND lease_expires_at>? AND superseded=0`, generation, team, ref.RunID, ref.NodeID, previous, tx.time.UnixNano())
	return err
}
