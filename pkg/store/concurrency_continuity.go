package store

import (
	"context"
	"errors"
	"time"
)

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
	now = tx.now()
	if !expires.After(now) {
		return holderRefusal()
	}
	_, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at = CASE WHEN lease_expires_at < ? THEN ? ELSE lease_expires_at END
WHERE team = ? AND run_id = ? AND (? = '' OR node_id = ? OR node_id = '') AND lease_expires_at > ? AND superseded = 0
AND EXISTS (SELECT 1 FROM runs r WHERE r.team = concurrency_holders.team AND r.id = concurrency_holders.run_id AND r.finished_at IS NULL)
AND (node_id = '' OR EXISTS (SELECT 1 FROM nodes n WHERE n.team = concurrency_holders.team AND n.run_id = concurrency_holders.run_id AND n.node_id = concurrency_holders.node_id AND n.status <> 'done'))`, expires.UnixNano(), expires.UnixNano(), team, runID, nodeID, nodeID, now.UnixNano())
	if err != nil || nodeID != "" {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE concurrency_waiters SET lease_expires_at = CASE WHEN lease_expires_at < ? THEN ? ELSE lease_expires_at END
WHERE team = ? AND run_id = ? AND lease_expires_at > ?
AND EXISTS (SELECT 1 FROM runs r WHERE r.team = concurrency_waiters.team AND r.id = concurrency_waiters.run_id AND r.finished_at IS NULL)
AND (node_id = '' OR EXISTS (SELECT 1 FROM nodes n WHERE n.team = concurrency_waiters.team AND n.run_id = concurrency_waiters.run_id AND n.node_id = concurrency_waiters.node_id AND n.status <> 'done'))`, expires.UnixNano(), expires.UnixNano(), team, runID, now.UnixNano())
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
