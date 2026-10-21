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
func renewCapacityTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, expires time.Time) error {
	if err := lockBoundCapacityTx(ctx, tx, team, runID, nodeID, true); err != nil {
		return err
	}
	now, err := tx.now(ctx)
	if err != nil {
		return err
	}
	if !expires.After(now) {
		return holderRefusal()
	}
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		if table == "concurrency_waiters" && nodeID != "" {
			continue
		}
		superseded := ""
		if table == "concurrency_holders" {
			superseded = " AND superseded=0"
		}
		rows, err := tx.QueryContext(ctx, `SELECT key,node_id,holder_id,lease_expires_at FROM `+table+` AS leased
WHERE team=? AND run_id=? AND (?='' OR node_id=? OR node_id='') AND lease_expires_at>?`+superseded+`
AND EXISTS (SELECT 1 FROM runs r WHERE r.team=leased.team AND r.id=leased.run_id AND r.finished_at IS NULL)
AND (node_id='' OR EXISTS (SELECT 1 FROM nodes n WHERE n.team=leased.team AND n.run_id=leased.run_id AND n.node_id=leased.node_id AND n.status<>'done' AND n.cancel_requested_at IS NULL))`, team, runID, nodeID, nodeID, now.UnixNano())
		if err != nil {
			return err
		}
		type renewal struct {
			key, nodeID, holderID string
			expires               int64
		}
		var renewals []renewal
		for rows.Next() {
			var r renewal
			if err := rows.Scan(&r.key, &r.nodeID, &r.holderID, &r.expires); err != nil {
				return errors.Join(err, rows.Close())
			}
			renewals = append(renewals, r)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range renewals {
			bound, err := capacityLeaseCeilingTx(ctx, tx, team, runID, r.nodeID, max(r.expires, expires.UnixNano()))
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET lease_expires_at=? WHERE team=? AND key=? AND run_id=? AND node_id=? AND holder_id=?`, bound, team, r.key, runID, r.nodeID, r.holderID); err != nil {
				return err
			}
		}
	}
	return nil
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
	if err := lockBoundCapacityTx(ctx, tx, team, string(ref.RunID), string(ref.NodeID), false); err != nil {
		return err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return clockErr2
	}
	_, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET generation=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND lease_expires_at>? AND superseded=0`, generation, team, ref.RunID, ref.NodeID, previous, clockTime2.UnixNano())
	return err
}
