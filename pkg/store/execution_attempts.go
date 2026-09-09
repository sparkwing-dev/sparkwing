package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type ExecutionStart struct {
	HolderID        string `json:"holder_id"`
	MembershipID    string `json:"membership_id,omitempty"`
	ReservationID   string `json:"reservation_id,omitempty"`
	ClaimGeneration int64  `json:"claim_generation"`
	AttemptOrdinal  int    `json:"attempt_ordinal"`
	// ExecutorKind is [ExecutorKindLocal] when the dispatcher runs the node
	// in its own process, which has no claim identity to carry. Every other
	// value is ignored: a claimed attempt takes its attribution from the
	// executor that won the node.
	ExecutorKind string `json:"executor_kind,omitempty"`
	// ExecutorID names the host the node ran on, and is required alongside a
	// local ExecutorKind.
	ExecutorID string `json:"executor_id,omitempty"`
}

type ExecutionAttemptFinish struct {
	HolderID        string `json:"holder_id"`
	MembershipID    string `json:"membership_id,omitempty"`
	ReservationID   string `json:"reservation_id,omitempty"`
	ClaimGeneration int64  `json:"claim_generation"`
	AttemptOrdinal  int    `json:"attempt_ordinal"`
	Outcome         string `json:"outcome"`
	FailureReason   string `json:"failure_reason,omitempty"`
	// ExecutorKind closes the attempt [ExecutionStart] opened with the same
	// kind, and must be [ExecutorKindLocal] for a locally executed node.
	ExecutorKind string `json:"executor_kind,omitempty"`
}

type ExecutionAttempt struct {
	RunID            string     `json:"run_id"`
	NodeID           string     `json:"node_id,omitempty"`
	Attempt          int        `json:"attempt"`
	ClaimGeneration  int64      `json:"claim_generation,omitempty"`
	CoordinatorID    string     `json:"-"`
	MembershipID     string     `json:"-"`
	ExecutorKind     string     `json:"executor_kind,omitempty"`
	ExecutorName     string     `json:"executor_name,omitempty"`
	ExecutorID       string     `json:"-"`
	ExecutorLocation string     `json:"location,omitempty"`
	HolderID         string     `json:"-"`
	ReservationID    string     `json:"-"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	Outcome          string     `json:"outcome,omitempty"`
	FailureReason    string     `json:"failure_reason,omitempty"`
	RetryRunID       string     `json:"retry_run_id,omitempty"`
}

func executionAttributionEventFields(kind, name, location string) map[string]any {
	fields := map[string]any{"location": location}
	if kind != "" {
		fields["executor_kind"] = kind
	}
	if kind != "" && name != "" {
		fields["executor_name"] = name
	}
	return fields
}

func (s *Store) AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, start ExecutionStart) error {
	if triggerFence, triggerClaim := TriggerClaimFenceFromContext(ctx); triggerClaim {
		if _, nodeClaim := NodeClaimFenceFromContext(ctx); nodeClaim {
			return ErrLockHeld
		}
		return s.acknowledgeTriggerExecutionStart(ctx, runID, nodeID, triggerFence, start)
	}
	if start.ExecutorKind == ExecutorKindLocal {
		return s.startLocalNodeExecutionAttempt(ctx, runID, nodeID, start.ExecutorID, start.AttemptOrdinal)
	}
	if start.HolderID == "" || start.ClaimGeneration < 1 || start.AttemptOrdinal < 1 {
		return ErrLockHeld
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var holder, principal, prefix, coordinator, membership, kind, executorName, executor, location, reservation, root string
	var generation int64
	var consumed int
	var lease sql.NullInt64
	var status, outcome string
	err = tx.QueryRowContext(ctx, `SELECT claimed_by, claim_principal, claim_token_prefix,
       coordinator_id, claim_membership_id, executor_kind, claim_worker_id, executor_id, executor_location,
       reservation_id, retry_root_run_id, claim_generation, attempts_consumed,
       lease_expires_at, status, outcome
  FROM nodes WHERE run_id = ? AND node_id = ?`+s.forUpdate(), runID, nodeID).Scan(
		&holder, &principal, &prefix, &coordinator, &membership, &kind, &executorName, &executor, &location,
		&reservation, &root, &generation, &consumed, &lease, &status, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if holder != start.HolderID || principal != claimant.Principal || prefix != claimant.TokenPrefix ||
		membership != start.MembershipID || reservation != start.ReservationID || generation != start.ClaimGeneration ||
		!lease.Valid || lease.Int64 <= now.UnixNano() || status == nodeStatusDone || outcome != "" {
		return ErrLockHeld
	}
	if root == "" {
		root = runID
	}
	var prior ExecutionAttempt
	var priorStarted int64
	err = tx.QueryRowContext(ctx, `SELECT run_id, claim_generation, coordinator_id, membership_id,
       executor_kind, executor_name, executor_id, executor_location, holder_id, reservation_id, started_at
  FROM node_execution_attempts
 WHERE lineage_root_run_id = ? AND node_id = ? AND attempt_ordinal = ?`,
		root, nodeID, start.AttemptOrdinal).Scan(&prior.RunID, &prior.ClaimGeneration, &prior.CoordinatorID,
		&prior.MembershipID, &prior.ExecutorKind, &prior.ExecutorName, &prior.ExecutorID, &prior.ExecutorLocation,
		&prior.HolderID, &prior.ReservationID, &priorStarted)
	if err == nil {
		if prior.RunID == runID && prior.ClaimGeneration == generation &&
			prior.CoordinatorID == coordinator && prior.MembershipID == membership &&
			prior.ExecutorKind == kind && prior.ExecutorName == executorName &&
			prior.ExecutorID == executor && prior.ExecutorLocation == location &&
			prior.HolderID == holder && prior.ReservationID == reservation && consumed == start.AttemptOrdinal {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if start.AttemptOrdinal != consumed+1 {
		return ErrLockHeld
	}
	if location == "" {
		location = "unknown"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_execution_attempts
    (lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
     coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
     holder_id, reservation_id, started_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		root, runID, nodeID, start.AttemptOrdinal, generation, coordinator, membership,
		kind, executorName, executor, location, holder, reservation, now.UnixNano()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes
   SET attempts_consumed = ?, execution_started_at = COALESCE(execution_started_at, ?)
 WHERE run_id = ? AND node_id = ? AND claim_generation = ? AND attempts_consumed = ?`,
		start.AttemptOrdinal, now.UnixNano(), runID, nodeID, generation, consumed)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLockHeld
	}
	event := executionAttributionEventFields(kind, executorName, location)
	event["attempt"] = start.AttemptOrdinal
	event["claim_generation"] = generation
	payload, _ := json.Marshal(event)
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_started", payload, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) acknowledgeTriggerExecutionStart(ctx context.Context, runID, nodeID string, fence TriggerClaimFence, start ExecutionStart) error {
	ordinal := start.AttemptOrdinal
	if ordinal < 1 || fence.ClaimGeneration < 1 {
		return ErrLockHeld
	}
	localExecutor := ""
	if start.ExecutorKind == ExecutorKindLocal {
		if start.ExecutorID == "" || len(start.ExecutorID) > maxLocalExecutorIDLen {
			return ErrLockHeld
		}
		localExecutor = start.ExecutorID
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertRunMutationFenceTx(ctx, tx, runID); err != nil {
		return err
	}
	var consumed int
	var root, status, outcome, location string
	if err := tx.QueryRowContext(ctx, `SELECT attempts_consumed, retry_root_run_id,
       status, outcome, executor_location FROM nodes WHERE run_id = ? AND node_id = ?`+s.forUpdate(),
		runID, nodeID).Scan(&consumed, &root, &status, &outcome, &location); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("node", runID+"/"+nodeID)
		}
		return err
	}
	if status == nodeStatusDone || outcome != "" {
		return ErrLockHeld
	}
	if root == "" {
		root = runID
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return err
	}
	if location != "local" && location != "cloud" {
		location = "unknown"
	}
	kind, executorName := "", ""
	if localExecutor != "" {
		kind, executorName, location = ExecutorKindLocal, localExecutor, executorLocationLocal
	}
	holder := "trigger:" + coordinatorID
	var priorRun, priorCoordinator, priorKind, priorName, priorExecutor, priorLocation, priorHolder string
	var priorGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT run_id, claim_generation, coordinator_id,
       executor_kind, executor_name, executor_id, executor_location, holder_id
  FROM node_execution_attempts
 WHERE lineage_root_run_id = ? AND node_id = ? AND attempt_ordinal = ?`,
		root, nodeID, ordinal).Scan(&priorRun, &priorGeneration, &priorCoordinator,
		&priorKind, &priorName, &priorExecutor, &priorLocation, &priorHolder)
	if err == nil {
		if priorRun == runID && priorGeneration == fence.ClaimGeneration &&
			priorCoordinator == coordinatorID && priorKind == kind && priorName == executorName &&
			priorExecutor == coordinatorID && priorLocation == location &&
			priorHolder == holder && consumed == ordinal {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ordinal != consumed+1 {
		return ErrLockHeld
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_execution_attempts
    (lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
     coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
     holder_id, reservation_id, started_at)
VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, '', ?)`,
		root, runID, nodeID, ordinal, fence.ClaimGeneration, coordinatorID,
		kind, executorName, coordinatorID, location, holder, now.UnixNano()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes
   SET attempts_consumed = ?, execution_started_at = COALESCE(execution_started_at, ?)
 WHERE run_id = ? AND node_id = ? AND attempts_consumed = ?`,
		ordinal, now.UnixNano(), runID, nodeID, consumed)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrLockHeld
	}
	event := executionAttributionEventFields(kind, executorName, location)
	event["attempt"] = ordinal
	event["claim_generation"] = fence.ClaimGeneration
	payload, _ := json.Marshal(event)
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_started", payload, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, claimant ClaimIdentity, finish ExecutionAttemptFinish) error {
	if triggerFence, triggerClaim := TriggerClaimFenceFromContext(ctx); triggerClaim {
		if _, nodeClaim := NodeClaimFenceFromContext(ctx); nodeClaim {
			return ErrLockHeld
		}
		return s.finishTriggerExecutionAttempt(ctx, runID, nodeID, triggerFence, finish)
	}
	if finish.ExecutorKind == ExecutorKindLocal {
		return s.finishLocalNodeExecutionAttempt(ctx, runID, nodeID, finish)
	}
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var finished sql.NullInt64
	var outcome, failureReason string
	err = tx.QueryRowContext(ctx, `SELECT a.finished_at, a.outcome, a.failure_reason
  FROM node_execution_attempts a
  JOIN nodes n ON n.run_id = a.run_id AND n.node_id = a.node_id
 WHERE a.run_id = ? AND a.node_id = ? AND a.claim_generation = ? AND a.attempt_ordinal = ?
   AND a.holder_id = ? AND a.membership_id = ? AND a.reservation_id = ?
	  AND n.claimed_by = ? AND n.claim_principal = ? AND n.claim_token_prefix = ?
	  AND n.claim_generation = ? AND n.claim_membership_id = ? AND n.reservation_id = ?
	  AND `+nodeClaimLiveSQL("n.")+` AND n.`+nodeNotDone+s.forUpdate(),
		runID, nodeID, finish.ClaimGeneration, finish.AttemptOrdinal,
		finish.HolderID, finish.MembershipID, finish.ReservationID,
		finish.HolderID, claimant.Principal, claimant.TokenPrefix,
		finish.ClaimGeneration, finish.MembershipID, finish.ReservationID, now).Scan(
		&finished, &outcome, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	if finished.Valid {
		if outcome == finish.Outcome && failureReason == finish.FailureReason {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	res, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET finished_at = ?, outcome = ?, failure_reason = ?
 WHERE run_id = ? AND node_id = ? AND claim_generation = ? AND attempt_ordinal = ?
   AND finished_at IS NULL`, now, finish.Outcome, finish.FailureReason,
		runID, nodeID, finish.ClaimGeneration, finish.AttemptOrdinal)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrLockHeld
	}
	payload, _ := json.Marshal(map[string]any{
		"attempt": finish.AttemptOrdinal, "claim_generation": finish.ClaimGeneration,
		"outcome": finish.Outcome, "failure_reason": finish.FailureReason,
	})
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_finished", payload, time.Unix(0, now)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) finishTriggerExecutionAttempt(ctx context.Context, runID, nodeID string, fence TriggerClaimFence, finish ExecutionAttemptFinish) error {
	if finish.AttemptOrdinal < 1 || fence.ClaimGeneration < 1 {
		return ErrLockHeld
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertRunMutationFenceTx(ctx, tx, runID); err != nil {
		return err
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return err
	}
	var finished sql.NullInt64
	var outcome, failureReason string
	err = tx.QueryRowContext(ctx, `SELECT finished_at, outcome, failure_reason
  FROM node_execution_attempts
 WHERE run_id = ? AND node_id = ? AND claim_generation = ? AND attempt_ordinal = ?
   AND coordinator_id = ? AND executor_id = ? AND holder_id = ?`+s.forUpdate(),
		runID, nodeID, fence.ClaimGeneration, finish.AttemptOrdinal,
		coordinatorID, coordinatorID, "trigger:"+coordinatorID).Scan(
		&finished, &outcome, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	if finished.Valid {
		if outcome == finish.Outcome && failureReason == finish.FailureReason {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	now := time.Now()
	res, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET finished_at = ?, outcome = ?, failure_reason = ?
 WHERE run_id = ? AND node_id = ? AND claim_generation = ? AND attempt_ordinal = ?
   AND finished_at IS NULL`, now.UnixNano(), finish.Outcome, finish.FailureReason,
		runID, nodeID, fence.ClaimGeneration, finish.AttemptOrdinal)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrLockHeld
	}
	payload, _ := json.Marshal(map[string]any{
		"attempt": finish.AttemptOrdinal, "claim_generation": fence.ClaimGeneration,
		"outcome": finish.Outcome, "failure_reason": finish.FailureReason,
	})
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_finished", payload, now); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: an in-process dispatcher's attempts must be distinguishable from
// the trigger-owned ones a coordinator opens for a remote executor, because
// only the holder identity tells the two finish paths apart.
const localAttemptHolderPrefix = "local:"

// safety: the id is written to two columns and read back on every finish, so
// an unbounded one from the wire would be stored unbounded.
const maxLocalExecutorIDLen = 128

func (s *Store) startLocalNodeExecutionAttempt(ctx context.Context, runID, nodeID, executorID string, ordinal int) (err error) {
	if ordinal < 1 || executorID == "" || len(executorID) > maxLocalExecutorIDLen {
		return ErrLockHeld
	}
	if _, nodeClaim := NodeClaimFenceFromContext(ctx); nodeClaim {
		return ErrLockHeld
	}
	var generation int64
	if fence, ok := TriggerClaimFenceFromContext(ctx); ok {
		generation = fence.ClaimGeneration
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := s.assertRunMutationFenceTx(ctx, tx, runID); err != nil {
		return err
	}
	var root, status, outcome, claimed string
	if err := tx.QueryRowContext(ctx, `SELECT retry_root_run_id, status, outcome,
       COALESCE(claimed_by, '') FROM nodes WHERE run_id = ? AND node_id = ?`+s.forUpdate(),
		runID, nodeID).Scan(&root, &status, &outcome, &claimed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("node", runID+"/"+nodeID)
		}
		return err
	}
	if status == nodeStatusDone || outcome != "" || claimed != "" {
		return ErrLockHeld
	}
	if root == "" {
		root = runID
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return err
	}
	holder := localAttemptHolderPrefix + coordinatorID
	var priorRun, priorCoordinator, priorKind, priorName, priorExecutor, priorLocation, priorHolder string
	var priorGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT run_id, claim_generation, coordinator_id,
       executor_kind, executor_name, executor_id, executor_location, holder_id
  FROM node_execution_attempts
 WHERE lineage_root_run_id = ? AND node_id = ? AND attempt_ordinal = ?`,
		root, nodeID, ordinal).Scan(&priorRun, &priorGeneration, &priorCoordinator,
		&priorKind, &priorName, &priorExecutor, &priorLocation, &priorHolder)
	if err == nil {
		if priorRun == runID && priorGeneration == generation && priorCoordinator == coordinatorID &&
			priorKind == ExecutorKindLocal && priorName == executorID && priorExecutor == executorID &&
			priorLocation == executorLocationLocal && priorHolder == holder {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_ordinal), 0)
  FROM node_execution_attempts WHERE lineage_root_run_id = ? AND node_id = ?`,
		root, nodeID).Scan(&recorded); err != nil {
		return err
	}
	// safety: the caller numbers its own attempts against the node's retry
	// budget, which can already be part spent, so a new attempt only has to
	// come after every attempt recorded -- not immediately after it.
	if ordinal <= recorded {
		return ErrLockHeld
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_execution_attempts
    (lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
     coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
     holder_id, reservation_id, started_at)
VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, '', ?)`,
		root, runID, nodeID, ordinal, generation, coordinatorID,
		ExecutorKindLocal, executorID, executorID, executorLocationLocal,
		holder, now.UnixNano()); err != nil {
		return err
	}
	// safety: a local attempt records where the node ran, and must not spend
	// the retry budget the claimed paths meter with attempts_consumed: a
	// bounced node's replacement process would find that budget gone.
	res, err := tx.ExecContext(ctx, `UPDATE nodes
   SET execution_started_at = COALESCE(execution_started_at, ?)
 WHERE run_id = ? AND node_id = ?`, now.UnixNano(), runID, nodeID)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrLockHeld
	}
	event := executionAttributionEventFields(ExecutorKindLocal, executorID, executorLocationLocal)
	event["attempt"] = ordinal
	event["claim_generation"] = generation
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_started", event, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) finishLocalNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish ExecutionAttemptFinish) (err error) {
	if finish.AttemptOrdinal < 1 {
		return ErrLockHeld
	}
	if _, nodeClaim := NodeClaimFenceFromContext(ctx); nodeClaim {
		return ErrLockHeld
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := s.assertRunMutationFenceTx(ctx, tx, runID); err != nil {
		return err
	}
	coordinatorID, err := coordinatorIDTx(ctx, tx)
	if err != nil {
		return err
	}
	var finished sql.NullInt64
	var outcome, failureReason string
	err = tx.QueryRowContext(ctx, `SELECT finished_at, outcome, failure_reason
  FROM node_execution_attempts
 WHERE run_id = ? AND node_id = ? AND attempt_ordinal = ?
   AND coordinator_id = ? AND executor_kind = ? AND holder_id = ?`+s.forUpdate(),
		runID, nodeID, finish.AttemptOrdinal, coordinatorID, ExecutorKindLocal,
		localAttemptHolderPrefix+coordinatorID).Scan(&finished, &outcome, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	if finished.Valid {
		if outcome == finish.Outcome && failureReason == finish.FailureReason {
			return tx.Commit()
		}
		return ErrLockHeld
	}
	now := time.Now()
	res, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET finished_at = ?, outcome = ?, failure_reason = ?
 WHERE run_id = ? AND node_id = ? AND attempt_ordinal = ?
   AND executor_kind = ? AND holder_id = ? AND finished_at IS NULL`,
		now.UnixNano(), finish.Outcome, finish.FailureReason,
		runID, nodeID, finish.AttemptOrdinal, ExecutorKindLocal,
		localAttemptHolderPrefix+coordinatorID)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return ErrLockHeld
	}
	event := map[string]any{
		"attempt": finish.AttemptOrdinal, "outcome": finish.Outcome,
		"failure_reason": finish.FailureReason,
	}
	if _, err := appendEventTx(ctx, tx, runID, nodeID, "execution_attempt_finished", event, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListNodeExecutionAttempts(ctx context.Context, runID, nodeID string) ([]ExecutionAttempt, error) {
	var root string
	err := s.queryRow(ctx, `SELECT retry_root_run_id FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return nil, err
	}
	if root == "" {
		root = runID
	}
	rows, err := s.query(ctx, `SELECT run_id, node_id, attempt_ordinal, claim_generation,
       coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location,
       holder_id, reservation_id, started_at, finished_at, outcome, failure_reason, retry_run_id
  FROM node_execution_attempts
 WHERE lineage_root_run_id = ? AND node_id = ?
 ORDER BY attempt_ordinal`, root, nodeID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ExecutionAttempt
	for rows.Next() {
		var item ExecutionAttempt
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&item.RunID, &item.NodeID, &item.Attempt, &item.ClaimGeneration,
			&item.CoordinatorID, &item.MembershipID, &item.ExecutorKind, &item.ExecutorName, &item.ExecutorID, &item.ExecutorLocation,
			&item.HolderID, &item.ReservationID, &started, &finished,
			&item.Outcome, &item.FailureReason, &item.RetryRunID); err != nil {
			return nil, err
		}
		item.StartedAt = time.Unix(0, started)
		if finished.Valid {
			t := time.Unix(0, finished.Int64)
			item.FinishedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) TriggerExecutionAttemptIsLive(ctx context.Context, runID, nodeID string, fence TriggerClaimFence, ordinal int, now time.Time) (bool, error) {
	if ordinal < 1 || fence.ClaimGeneration < 1 {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx, `SELECT 1
  FROM node_execution_attempts a
  JOIN triggers t ON t.id = a.run_id
	WHERE a.run_id = ? AND a.node_id = ? AND a.attempt_ordinal = ?
	  AND a.claim_generation = ? AND a.holder_id = 'trigger:' || a.coordinator_id
	  AND a.finished_at IS NULL
	  AND t.claim_principal = ? AND t.claim_token_prefix = ?
	  AND t.claim_seq = ? AND `+triggerClaimLiveSQL("t."), runID, nodeID, ordinal,
		fence.ClaimGeneration, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
		fence.ClaimGeneration, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
