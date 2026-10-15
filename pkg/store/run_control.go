package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Run admission states of a controller-dispatched run. A run that predates
// them reads as "". Plan-level concurrency, which would hold a run queued, is
// refused at plan acceptance, so an accepted plan is admitted at once.
const (
	RunAdmissionPlanning = "planning"
	RunAdmissionAdmitted = "admitted"
)

const approvalControllerActor = "sparkwing"

type approvalSpec struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

func openApprovalTx(ctx context.Context, tx *storeTx, team Team, runID string, n *settleNode, now time.Time) error {
	var spec approvalSpec
	if err := json.Unmarshal(n.approval, &spec); err != nil {
		return fmt.Errorf("node %s/%s approval: %w", runID, n.id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approvals (team, run_id, node_id, requested_at, message, timeout_ms, on_timeout)
VALUES (?, ?, ?, ?, ?, ?, ?)`, string(team), runID, n.id, now.UnixNano(), spec.Message, spec.TimeoutMS, spec.OnTimeout); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = ?, started_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status = ?`,
		NodeStatusApprovalPending, now.UnixNano(), string(team), runID, n.id, nodeStatusPending)
	return err
}

func stampApprovalTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, resolution, approver, comment string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE approvals SET resolved_at = ?, resolution = ?, approver = ?, comment = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND resolved_at IS NULL`,
		now.UnixNano(), resolution, approver, comment, string(team), runID, nodeID)
	return err
}

// safety: a timeout follows the gate's on_timeout policy, so a gate that
// approves on timeout succeeds.
func approvalOutcome(resolution, approver, comment, onTimeout string) (string, string) {
	switch resolution {
	case ApprovalResolutionApproved:
		return outcomeSuccess, ""
	case ApprovalResolutionTimedOut:
		if onTimeout == ApprovalOnTimeoutApprove {
			return outcomeSuccess, ""
		}
		if onTimeout == "" {
			onTimeout = ApprovalOnTimeoutFail
		}
		return outcomeFailed, "approval timed out (policy=" + onTimeout + ")"
	case ApprovalResolutionDenied:
		msg := "denied by " + approver
		if comment != "" {
			msg += ": " + comment
		}
		return outcomeFailed, msg
	}
	return outcomeFailed, "unknown approval resolution: " + resolution
}

// safety: the run lock serializes competing gate resolutions.
func resolveApprovalTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, resolution, approver, comment string,
	now time.Time,
) (bool, error) {
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return false, err
	}
	var onTimeout string
	err := tx.QueryRowContext(ctx, `SELECT a.on_timeout FROM approvals a
  JOIN nodes n ON n.team = a.team AND n.run_id = a.run_id AND n.node_id = a.node_id
 WHERE a.team = ? AND a.run_id = ? AND a.node_id = ? AND a.resolved_at IS NULL AND n.status = ?`,
		string(team), runID, nodeID, NodeStatusApprovalPending).Scan(&onTimeout)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := stampApprovalTx(ctx, tx, team, runID, nodeID, resolution, approver, comment, now); err != nil {
		return false, err
	}
	outcome, msg := approvalOutcome(resolution, approver, comment, onTimeout)
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status = ?, outcome = ?, error = ?, finished_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status = ?`,
		nodeStatusDone, outcome, msg, now.UnixNano(), string(team), runID, nodeID, NodeStatusApprovalPending); err != nil {
		return false, err
	}
	return true, settleTx(ctx, tx, team, runID, now)
}

// safety: any node but a controller approval gate reports handled false and
// writes nothing, so a gate the in-process dispatcher polls keeps its path.
func (s *Store) resolveControllerApproval(ctx context.Context, team Team, runID, nodeID, resolution, approver, comment string) (handled bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	// safety: a node's kind is fixed when it is inserted, so reading it before
	// the run lock cannot misroute the resolution.
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT kind FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && kind != nodeKindApproval) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resolved, err := resolveApprovalTx(ctx, tx, team, runID, nodeID, resolution, approver, comment, time.Now())
	if err != nil {
		return true, err
	}
	if !resolved {
		return true, ErrLockHeld
	}
	return true, tx.Commit()
}

type teamNodeRef struct {
	team          Team
	runID, nodeID string
}

// safety: a run with no controller planning node reports handled false and
// writes nothing, so a run a trigger holder drives keeps the cooperative cancel.
func (s *Store) requestControllerRunCancel(ctx context.Context, team Team, runID string, now time.Time) (handled bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE team = ? AND run_id = ? AND node_id = ? AND kind = ?`,
		string(team), runID, PlanNodeID, nodeKindPlan).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET cancel_requested_at = COALESCE(cancel_requested_at, ?)
 WHERE team = ? AND id = ? AND NOT (`+runTerminalIn+`)`, now.UnixNano(), string(team), runID); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET cancel_requested_at = COALESCE(cancel_requested_at, ?)
 WHERE team = ? AND run_id = ? AND claimed_by IS NOT NULL AND `+nodeNotDone,
		now.UnixNano(), string(team), runID); err != nil {
		return true, err
	}
	if err := settleTx(ctx, tx, team, runID, now); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

// safety: a node of a run with a cancel request is never awarded, so a claim
// that ends on such a run cannot be replaced by a new one.
const nodeRunNotCancelled = ` AND NOT EXISTS (SELECT 1 FROM runs cr
     WHERE cr.team = nodes.team AND cr.id = nodes.run_id AND cr.cancel_requested_at IS NOT NULL)`
