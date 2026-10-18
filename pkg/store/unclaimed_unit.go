package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// UnclaimedUnit is a live run's longest-waiting unit that no claimant holds.
type UnclaimedUnit struct {
	Kind   capability.UnitKind
	NodeID string
	Since  time.Time
}

func readUnclaimedUnitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*UnclaimedUnit, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode != runspec.IntakeExecute {
		return nil, nil
	}
	query := `SELECT unit_kind AS kind,'' AS node,available_at AS since FROM triggers WHERE team=? AND id=? AND status='pending' AND claimed_by IS NULL AND cancel_requested_at IS NULL`
	args := []any{team, run.ID}
	if run.Dispatch == string(runspec.DispatchController) {
		query += ` UNION ALL SELECT 'node',node_id,ready_at FROM nodes WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND claimed_by IS NULL AND status='pending' AND cancel_requested_at IS NULL
 UNION ALL SELECT 'eval',e.source_node_id,n.finished_at FROM eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id
 WHERE e.team=? AND e.run_id=? AND e.status='pending' AND e.claimed_by IS NULL AND n.finished_at IS NOT NULL`
		args = append(args, team, run.ID, team, run.ID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,node,since FROM (`+query+`) AS waiting ORDER BY since LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var unit UnclaimedUnit
	var kind string
	var since int64
	if err := rows.Scan(&kind, &unit.NodeID, &since); err != nil {
		return nil, err
	}
	unit.Kind, unit.Since = capability.UnitKind(kind), time.Unix(0, since).UTC()
	return &unit, rows.Err()
}
