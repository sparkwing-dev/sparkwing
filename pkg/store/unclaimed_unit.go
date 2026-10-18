package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// UnclaimedUnit is a live run's longest-waiting unit that no claimant holds.
type UnclaimedUnit struct {
	Kind   capability.UnitKind
	NodeID string
	Since  time.Time
	Needs  []string
}

func readUnclaimedUnitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*UnclaimedUnit, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode != runspec.IntakeExecute {
		return nil, nil
	}
	var oldest *UnclaimedUnit
	keep := func(unit UnclaimedUnit) {
		if oldest == nil || unit.Since.Before(oldest.Since) {
			oldest = &unit
		}
	}
	var kind string
	var since int64
	var needs []byte
	err := tx.QueryRowContext(ctx, `SELECT unit_kind,available_at,needs_labels FROM triggers WHERE team=? AND id=? AND status='pending' AND claimed_by IS NULL AND cancel_requested_at IS NULL`, team, run.ID).Scan(&kind, &since, &needs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		unit := UnclaimedUnit{Kind: capability.UnitKind(kind), Since: time.Unix(0, since).UTC()}
		if err := json.Unmarshal(needs, &unit.Needs); err != nil {
			return nil, err
		}
		keep(unit)
	}
	if run.Dispatch != string(runspec.DispatchController) {
		return oldest, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT 'node',node_id,ready_at,execution_json FROM nodes WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND claimed_by IS NULL AND status='pending' AND cancel_requested_at IS NULL
 UNION ALL SELECT 'eval',e.source_node_id,n.finished_at,n.execution_json FROM eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id
 WHERE e.team=? AND e.run_id=? AND e.status='pending' AND e.claimed_by IS NULL AND n.finished_at IS NOT NULL`, team, run.ID, team, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var unit UnclaimedUnit
		var execution []byte
		if err := rows.Scan(&kind, &unit.NodeID, &since, &execution); err != nil {
			return nil, err
		}
		var settings runspec.JobExecutionSettings
		if err := json.Unmarshal(execution, &settings); err != nil {
			return nil, err
		}
		unit.Kind, unit.Since, unit.Needs = capability.UnitKind(kind), time.Unix(0, since).UTC(), settings.Requires.Value
		keep(unit)
	}
	return oldest, rows.Err()
}
