package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Run admission states of a controller-dispatched run. A run that predates
// them reads as "". Plan-level concurrency, which would hold a run queued, is
// refused at plan acceptance, so an accepted plan is admitted at once.
const (
	RunAdmissionPlanning = "planning"
	RunAdmissionAdmitted = "admitted"
)

// safety: every v78 column is defaulted or nullable, so a binary predating it
// keeps writing runs and nodes, and none of them reads as cancelled.
var (
	runControlRunCols = map[string]string{
		"admission":           "TEXT NOT NULL DEFAULT ''",
		"cancel_requested_at": "INTEGER",
	}
	runControlNodeCols = map[string]string{
		"cancel_requested_at": "INTEGER",
		"approval_json":       "BLOB",
	}
)

const childInvocationsTable = `CREATE TABLE IF NOT EXISTS child_invocations (
    team             TEXT NOT NULL,
    parent_run_id    TEXT NOT NULL,
    parent_node_id   TEXT NOT NULL,
    claim_generation INTEGER NOT NULL,
    ordinal          INTEGER NOT NULL,
    request_digest   TEXT NOT NULL,
    child_run_id     TEXT NOT NULL,
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (team, parent_run_id, parent_node_id, claim_generation, ordinal),
    FOREIGN KEY (parent_run_id) REFERENCES runs(id) ON DELETE CASCADE
)`

func applyRunControlMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	if err := addDispatchColumnsTx(ctx, tx, postgres, map[string]map[string]string{
		"runs": runControlRunCols, "nodes": runControlNodeCols,
	}); err != nil {
		return err
	}
	ddl := childInvocationsTable
	if postgres {
		ddl = strings.ReplaceAll(ddl, "INTEGER", "BIGINT")
	}
	_, err := tx.ExecContext(ctx, ddl)
	return err
}

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

// safety: resolve and timeout both come here under the run row, so the first
// to commit decides the gate and the other finds it decided and writes
// nothing (false). With onlyExpired the gate is decided only once its timeout
// has passed at now.
func resolveApprovalTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, resolution, approver, comment string,
	now time.Time, onlyExpired bool,
) (bool, error) {
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return false, err
	}
	var onTimeout string
	var requestedAt, timeoutMS int64
	err := tx.QueryRowContext(ctx, `SELECT a.on_timeout, a.requested_at, a.timeout_ms FROM approvals a
  JOIN nodes n ON n.team = a.team AND n.run_id = a.run_id AND n.node_id = a.node_id
 WHERE a.team = ? AND a.run_id = ? AND a.node_id = ? AND a.resolved_at IS NULL AND n.status = ?`,
		string(team), runID, nodeID, NodeStatusApprovalPending).Scan(&onTimeout, &requestedAt, &timeoutMS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if onlyExpired && (timeoutMS <= 0 || requestedAt+timeoutMS*int64(time.Millisecond) >= now.UnixNano()) {
		return false, nil
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
	resolved, err := resolveApprovalTx(ctx, tx, team, runID, nodeID, resolution, approver, comment, time.Now(), false)
	if err != nil {
		return true, err
	}
	if !resolved {
		return true, ErrLockHeld
	}
	return true, tx.Commit()
}

const maxExpiredApprovalsPerPass = 500

type teamNodeRef struct {
	team          Team
	runID, nodeID string
}

func (s *Store) expiredControllerApprovals(ctx context.Context, now time.Time) ([]teamNodeRef, error) {
	rows, err := s.query(ctx, `SELECT a.team, a.run_id, a.node_id FROM approvals a
  JOIN nodes n ON n.team = a.team AND n.run_id = a.run_id AND n.node_id = a.node_id
 WHERE a.resolved_at IS NULL AND a.timeout_ms > 0 AND a.requested_at + (a.timeout_ms * 1000000) < ?
   AND n.kind = ? ORDER BY a.requested_at LIMIT ?`, now.UnixNano(), nodeKindApproval, maxExpiredApprovalsPerPass)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []teamNodeRef
	for rows.Next() {
		var e teamNodeRef
		if err := rows.Scan(&e.team, &e.runID, &e.nodeID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// safety: each gate is decided in a transaction of its own under its run row,
// so a pass never holds two runs' locks at once.
func (s *Store) timeOutControllerApprovals(ctx context.Context, now time.Time) ([][2]string, error) {
	expired, err := s.expiredControllerApprovals(ctx, now)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, e := range expired {
		var resolved bool
		err := s.inTx(ctx, func(tx *storeTx) (err error) {
			resolved, err = resolveApprovalTx(ctx, tx, e.team, e.runID, e.nodeID, ApprovalResolutionTimedOut,
				approvalControllerActor, "timeout enforced by controller", now, true)
			return err
		})
		if err != nil {
			return out, err
		}
		if resolved {
			out = append(out, [2]string{e.runID, e.nodeID})
		}
	}
	return out, nil
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

const maxExpiredDispatchRunsPerPass = 500

// safety: a controller-dispatched claim whose lease lapsed ends as a failed
// attempt of its node, so the node is retried within its budget, or cancelled
// on a cancelled run, and the run settles; each run is its own transaction,
// locking the run before its nodes and the ledger last.
func (s *Store) expireDispatchClaims(ctx context.Context, now time.Time) ([][2]string, error) {
	runs, err := s.expiredDispatchRuns(ctx, now)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, r := range runs {
		var expired [][2]string
		err := s.inTx(ctx, func(tx *storeTx) (err error) {
			expired, err = s.expireRunClaimsTx(ctx, tx, r.team, r.runID, now)
			return err
		})
		if err != nil {
			slog.Error("expire controller-dispatched claims", "team", r.team, "run", r.runID, "error", err)
			continue
		}
		out = append(out, expired...)
	}
	return out, nil
}

// safety: the orchestrator's expiry pass and this one are independent, so a
// run this pass cannot settle is logged and retried next pass, and never keeps
// the other pass from releasing its claims.
func (s *Store) expireDispatchClaimsLogged(ctx context.Context, now time.Time) [][2]string {
	out, err := s.expireDispatchClaims(ctx, now)
	if err != nil {
		slog.Error("expire controller-dispatched claims", "error", err)
	}
	return out
}

func (s *Store) expiredDispatchRuns(ctx context.Context, now time.Time) ([]teamNodeRef, error) {
	rows, err := s.query(ctx, `SELECT DISTINCT team, run_id FROM nodes
 WHERE kind != '' AND claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_expires_at < ?
   AND `+nodeNotDone+` ORDER BY team, run_id LIMIT ?`, now.UnixNano(), maxExpiredDispatchRunsPerPass)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []teamNodeRef
	for rows.Next() {
		var e teamNodeRef
		if err := rows.Scan(&e.team, &e.runID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) expireRunClaimsTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) ([][2]string, error) {
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return nil, err
	}
	type lapsed struct {
		nodeID, kind string
		generation   int64
		lease        int64
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id, kind, claim_generation, lease_expires_at FROM nodes
 WHERE team = ? AND run_id = ? AND kind != '' AND claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
   AND lease_expires_at < ? AND `+nodeNotDone+` ORDER BY node_id`, string(team), runID, now.UnixNano())
	if err != nil {
		return nil, err
	}
	var items []lapsed
	for rows.Next() {
		var l lapsed
		if err := rows.Scan(&l.nodeID, &l.kind, &l.generation, &l.lease); err != nil {
			closeRowsOrLog(rows)
			return nil, err
		}
		items = append(items, l)
	}
	closeRowsOrLog(rows)
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, team, runID)
	if err != nil {
		return nil, err
	}
	report := AttemptReport{Outcome: outcomeFailed, Error: "claim lease expired", FailureReason: FailureAgentLost, leaseLost: true}
	if cancelled {
		report.Outcome = outcomeCancelled
	}
	var out [][2]string
	for _, l := range items {
		tok := ClaimToken{Team: team, RunID: runID, NodeID: l.nodeID, Generation: l.generation, Kind: ClaimTokenKind(l.kind)}
		if err := s.commitAttemptBilledTx(ctx, tx, tok, report, now, time.Unix(0, min(l.lease, now.UnixNano()))); err != nil {
			return nil, err
		}
		out = append(out, [2]string{runID, l.nodeID})
	}
	return out, nil
}
