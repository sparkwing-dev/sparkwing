package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// RunRow is the immutable request/spec plus the mutable lifecycle read model used by hosts.
type RunRow struct {
	runstate.Execution
	Team       Team
	expansions []runstate.AcceptedExpansionRow
	admission  admissionIdentity
}

// RunSnapshot contains one consistent read, including accepted expansions in insertion order.
type RunSnapshot struct {
	Run        RunRow
	Nodes      []runstate.NodeRow
	Expansions []runstate.AcceptedExpansionRow
	StoreNow   time.Time
}

// ExecutionSnapshot omits persistence-only metadata at the host boundary.
func (s RunSnapshot) ExecutionSnapshot() runstate.RunSnapshot {
	return runstate.RunSnapshot{Run: runstate.RunRow{Execution: s.Run.Execution, Team: string(s.Run.Team)}, Nodes: s.Nodes, Expansions: s.Expansions, StoreNow: s.StoreNow}
}

// ReconcileRequest names a possible source; the store checks it rather than trusting the host's cache hit.
type ReconcileRequest struct {
	Ref    runspec.NodeRef
	Actor  runstate.Actor
	Policy runstate.SourcePolicy
	Source *runstate.MaterializeSource
	// MaterializeOnly returns Dispatch on a source miss without awarding execution.
	MaterializeOnly bool
}

// StateEvent uses a single per-run sequence and a unique node state sequence for lifecycle writes.
type StateEvent struct {
	Seq      int64
	StateSeq int64
	RunID    string
	NodeID   string
	Kind     string
	At       time.Time
	Payload  json.RawMessage
}

// ReadRun returns only the Tenant's run, including both seals and the stable team number.
func (t *Tenant) ReadRun(ctx context.Context, runID string) (RunRow, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunRow{}, err
	}
	defer rollbackOrLog(tx)
	return readRunTx(ctx, tx, t.team, runID)
}

// ReadNode returns persisted state without interpreting an elapsed lease as an implicit requeue.
func (t *Tenant) ReadNode(ctx context.Context, ref runspec.NodeRef) (runstate.NodeRow, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return runstate.NodeRow{}, err
	}
	defer rollbackOrLog(tx)
	return readNodeTx(ctx, tx, t.team, ref)
}

// ReadSnapshot prevents scheduling from a mix of states observed in different transactions.
func (t *Tenant) ReadSnapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	return readSnapshotTx(ctx, tx, t.team, runID)
}

// ReadStateEvents resumes strictly after the caller's sequence; state transitions own lifecycle event writes.
func (t *Tenant) ReadStateEvents(ctx context.Context, runID string, after int64, limit int) ([]StateEvent, error) {
	if after < 0 || limit <= 0 {
		return nil, errors.New("invalid event cursor or limit")
	}
	rows, err := t.s.query(ctx, `SELECT seq,state_seq,node_id,kind,ts,payload FROM events WHERE team=? AND run_id=? AND seq>? ORDER BY seq LIMIT ?`, t.team, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateEvent
	for rows.Next() {
		v := StateEvent{RunID: runID}
		var at int64
		var payload []byte
		if err := rows.Scan(&v.Seq, &v.StateSeq, &v.NodeID, &v.Kind, &at, &payload); err != nil {
			return nil, err
		}
		v.Payload = payload
		v.At = time.Unix(0, at).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReconcileNode decides and self-claims atomically; WaitHolder callers reconcile again after requeue.
// Materialization reads outcome and authorized result edges from persisted source state,
// never from a caller assertion. Source.Kind must match Policy; SourceNone forbids Source.
func (t *Tenant) ReconcileNode(ctx context.Context, request ReconcileRequest) (runstate.ReconciledNode, error) {
	return t.reconcileNode(ctx, request)
}

// stateEventRule makes every lifecycle event name store-owned on customer surfaces.
type stateEventRule struct {
	RequestOnly bool
	Cause       runstate.TransitionCause
	To          runstate.NodeState
	Kind        string
}

// nodeEvents matches requested To and RequestOnly; an empty cause matches every cause for that terminal outcome.
// Cancellation requests emit job_cancel_requested while preserving claimed/running state until the holder drains.
func nodeEvents() []stateEventRule {
	return []stateEventRule{
		{false, runstate.CausePublishAttempt, nodePending, "job_attempt_imported"},
		{false, runstate.CauseRelease, runstate.NodeReady, "job_ready"},
		{false, runstate.CauseRelease, nodePending, "job_requeued"},
		{false, runstate.CauseRevoke, nodePending, "job_revoked"},
		{false, runstate.CauseAward, runstate.NodeClaimed, "job_claimed"},
		{false, runstate.CauseApprovalOpen, runstate.NodeApprovalPending, "job_approval_pending"},
		{false, runstate.CauseStart, runstate.NodeRunning, "job_started"},
		{false, runstate.CauseRetry, runstate.NodeRunning, "job_retrying"},
		{false, runstate.CauseLeaseLapse, nodePending, "job_requeued"},
		{true, causeCancel, runstate.NodeCancelled, "job_cancel_requested"},
		{false, "", runstate.NodeSuccess, "job_succeeded"},
		{false, "", runstate.NodeFailed, "job_failed"},
		{false, "", runstate.NodeCancelled, "job_cancelled"},
		{false, "", runstate.NodeSkipped, "job_skipped"},
		{false, "", runstate.NodeSuperseded, "job_superseded"},
		{false, "", runstate.NodeMemoized, "job_memoized"},
	}
}

// safety: one advisory-lock spelling must serve all dialects to keep the declared lock order reviewable.
func advisoryLockTx(ctx context.Context, tx *storeTx, name string) error {
	if name == "" {
		return errors.New("empty lock name")
	}
	if tx.dialect == DialectPostgres {
		_, err := tx.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, name)
		return err
	}
	return nil
}
