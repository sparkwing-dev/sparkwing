package store

import (
	"context"
	"errors"
	"time"
)

// PlanNodeID identifies the legacy planning node.
const PlanNodeID = "plan"

// CompileLogNode identifies a trigger's compilation log.
const CompileLogNode = "_compile"

// MaxPlanBytes bounds legacy plan snapshots.
const MaxPlanBytes = 4 << 20

// MaxRetryBackoff bounds legacy disruption backoff.
const MaxRetryBackoff = 5 * time.Minute

// ErrPlanAccepted refuses replacement of an accepted plan snapshot.
var ErrPlanAccepted = errors.New("store: the run's plan was accepted from its planning claim")

const planNodeRetryBudget = 1

// CreatePlanNode inserts the legacy planning node for an empty run.
func (s *Store) CreatePlanNode(ctx context.Context, team Team, runID string, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return err
	}
	if err := insertPlanNodeTx(ctx, tx, team, runID, RepoDispatchController, now); err != nil {
		return err
	}
	return tx.Commit()
}

func insertPlanNodeTx(ctx context.Context, tx *storeTx, team Team, runID string, dispatch RepoDispatch, now time.Time) error {
	class, err := readyCPUClassTx(ctx, tx, ExecutorResource{})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes (team, run_id, node_id, status, kind, deps_json,
       credit_cpu_class, ready_at, placement_hold_from, retry_budget, seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		string(team), runID, PlanNodeID, nodeStatusPending, nodeKindPlan, []byte("[]"),
		class, now.UnixNano(), now.UnixNano(), planNodeRetryBudget); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET admission = ?, dispatch = ? WHERE team = ? AND id = ?`,
		RunAdmissionPlanning, string(dispatch), string(team), runID)
	return err
}
