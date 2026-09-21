package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	ClaimHolderHeader       = "X-Sparkwing-Claim-Holder"
	ClaimMembershipHeader   = "X-Sparkwing-Claim-Membership"
	ClaimReservationHeader  = "X-Sparkwing-Claim-Reservation"
	ClaimGenerationHeader   = "X-Sparkwing-Claim-Generation"
	AttemptOrdinalHeader    = "X-Sparkwing-Attempt-Ordinal"
	TriggerGenerationHeader = "X-Sparkwing-Trigger-Generation"

	// ClaimPollAfterHeader carries the whole number of seconds a controller
	// suggests a claim loop wait before polling again. It is advice a runner
	// may only widen its own cadence to, so a runner that ignores it polls
	// exactly as often as it was configured to.
	ClaimPollAfterHeader = "X-Sparkwing-Poll-After"

	// RunnerIdentityHeader names the runner behind a claim or heartbeat: a
	// holder id, an executor name, or another label stable for one runner
	// process. Runners sharing one token carry distinct values, so a
	// controller can budget them apart instead of budgeting the token.
	RunnerIdentityHeader = "X-Sparkwing-Runner"
)

type NodeClaimFence struct {
	Claimant        ClaimIdentity
	HolderID        string
	MembershipID    string
	ReservationID   string
	ClaimGeneration int64
}

type (
	nodeClaimFenceKey          struct{}
	executionAttemptOrdinalKey struct{}
	triggerClaimFenceKey       struct{}
)

type TriggerClaimFence struct {
	Claimant        ClaimIdentity
	ClaimGeneration int64
}

func WithNodeClaimFence(ctx context.Context, fence NodeClaimFence) context.Context {
	return context.WithValue(ctx, nodeClaimFenceKey{}, fence)
}

func NodeClaimFenceFromContext(ctx context.Context) (NodeClaimFence, bool) {
	fence, ok := ctx.Value(nodeClaimFenceKey{}).(NodeClaimFence)
	return fence, ok
}

func WithExecutionAttemptOrdinal(ctx context.Context, ordinal int) context.Context {
	return context.WithValue(ctx, executionAttemptOrdinalKey{}, ordinal)
}

func ExecutionAttemptOrdinalFromContext(ctx context.Context) (int, bool) {
	ordinal, ok := ctx.Value(executionAttemptOrdinalKey{}).(int)
	return ordinal, ok
}

func WithTriggerClaimFence(ctx context.Context, fence TriggerClaimFence) context.Context {
	return context.WithValue(ctx, triggerClaimFenceKey{}, fence)
}

func TriggerClaimFenceFromContext(ctx context.Context) (TriggerClaimFence, bool) {
	fence, ok := ctx.Value(triggerClaimFenceKey{}).(TriggerClaimFence)
	return fence, ok
}

func hasClaimFence(ctx context.Context) bool {
	_, node := NodeClaimFenceFromContext(ctx)
	_, trigger := TriggerClaimFenceFromContext(ctx)
	return node || trigger
}

func WithoutClaimFences(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, nodeClaimFenceKey{}, struct{}{})
	return context.WithValue(ctx, triggerClaimFenceKey{}, struct{}{})
}

func (s *Store) TriggerClaimFenceIsLive(ctx context.Context, runID string, claimant ClaimIdentity, generation int64, now time.Time) (bool, error) {
	var held int
	err := s.queryRow(ctx, `SELECT 1 FROM triggers
	WHERE id = ? AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_seq = ? AND `+triggerClaimLiveSQL(""), runID, claimant.Principal, claimant.TokenPrefix,
		generation, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) NodeClaimFenceIsLive(ctx context.Context, runID, nodeID string, fence NodeClaimFence, now time.Time) (bool, error) {
	var held int
	err := s.queryRow(ctx, `SELECT 1 FROM nodes
	WHERE run_id = ? AND node_id = ? AND claimed_by = ?
	  AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_membership_id = ? AND reservation_id = ? AND claim_generation = ?
	  AND `+nodeClaimLiveSQL(""),
		runID, nodeID, fence.HolderID, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
		fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) NodeExecutionAttemptIsLive(ctx context.Context, runID, nodeID string, fence NodeClaimFence, ordinal int, now time.Time) (bool, error) {
	if ordinal < 1 {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx, `SELECT 1 FROM nodes n
	WHERE n.run_id = ? AND n.node_id = ? AND n.claimed_by = ?
	  AND n.claim_principal = ? AND n.claim_token_prefix = ?
	  AND n.claim_membership_id = ? AND n.reservation_id = ? AND n.claim_generation = ?
		   AND `+nodeClaimLiveSQL("n.")+` AND n.`+nodeNotDone+`
	   AND EXISTS (SELECT 1 FROM node_execution_attempts a
       WHERE a.run_id = n.run_id AND a.node_id = n.node_id
         AND a.claim_generation = n.claim_generation AND a.attempt_ordinal = ?
         AND a.holder_id = n.claimed_by AND a.membership_id = n.claim_membership_id
		 AND a.reservation_id = n.reservation_id AND a.finished_at IS NULL)`,
		runID, nodeID, fence.HolderID, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
		fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, now.UnixNano(), ordinal).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// NodeExecutionAttemptBelongsToLiveClaim reports whether fence still holds the
// node's claim and owns the attempt at ordinal, whether or not that attempt has
// finished. An executor writes a node's closing lines after it closes the
// attempt, so a log append is authorized by the claim rather than by the
// attempt still being open; use [Store.NodeExecutionAttemptIsLive] where a
// running attempt is what matters.
func (s *Store) NodeExecutionAttemptBelongsToLiveClaim(ctx context.Context, runID, nodeID string, fence NodeClaimFence, ordinal int, now time.Time) (bool, error) {
	if ordinal < 1 {
		return false, nil
	}
	var held int
	err := s.queryRow(ctx, `SELECT 1 FROM nodes n
	WHERE n.run_id = ? AND n.node_id = ? AND n.claimed_by = ?
	  AND n.claim_principal = ? AND n.claim_token_prefix = ?
	  AND n.claim_membership_id = ? AND n.reservation_id = ? AND n.claim_generation = ?
		   AND `+nodeClaimLiveSQL("n.")+` AND n.`+nodeNotDone+`
	   AND EXISTS (SELECT 1 FROM node_execution_attempts a
       WHERE a.run_id = n.run_id AND a.node_id = n.node_id
         AND a.claim_generation = n.claim_generation AND a.attempt_ordinal = ?
         AND a.holder_id = n.claimed_by AND a.membership_id = n.claim_membership_id
		 AND a.reservation_id = n.reservation_id)`,
		runID, nodeID, fence.HolderID, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
		fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, now.UnixNano(), ordinal).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) assertNodeMutationFenceTx(ctx context.Context, tx *storeTx, runID, nodeID string) error {
	fence, ok := NodeClaimFenceFromContext(ctx)
	var held int
	var err error
	if ok {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
	WHERE run_id = ? AND node_id = ? AND claimed_by = ?
	  AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_membership_id = ? AND reservation_id = ? AND claim_generation = ?
	  AND `+nodeClaimLiveSQL("")+s.forUpdate(),
			runID, nodeID, fence.HolderID, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
			fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, time.Now().UnixNano()).Scan(&held)
	} else if _, triggerOK := TriggerClaimFenceFromContext(ctx); triggerOK {
		if err := s.assertRunMutationFenceTx(ctx, tx, DefaultTeam, runID); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
 WHERE run_id = ? AND node_id = ?`+s.forUpdate(), runID, nodeID).Scan(&held)
	} else {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) execNodeMutation(ctx context.Context, runID, nodeID, query string, args ...any) (rowsAffected, bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.assertNodeMutationFenceTx(ctx, tx, runID, nodeID); err != nil {
		return nil, false, err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	fenced := false
	if _, ok := NodeClaimFenceFromContext(ctx); ok {
		fenced = true
	}
	if _, ok := TriggerClaimFenceFromContext(ctx); ok {
		fenced = true
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return result, fenced, nil
}

// safety: the fence reads a tenant-owned table, so it takes the team of the
// handle that is mutating rather than matching an id across every team.
func (s *Store) assertRunMutationFenceTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	if _, nodeClaim := NodeClaimFenceFromContext(ctx); nodeClaim {
		return ErrLockHeld
	}
	fence, ok := TriggerClaimFenceFromContext(ctx)
	if !ok {
		return nil
	}
	var held int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM triggers
	WHERE team = ? AND id = ? AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_seq = ? AND `+triggerClaimLiveSQL("")+s.forUpdate(), string(team), runID,
		fence.Claimant.Principal, fence.Claimant.TokenPrefix,
		fence.ClaimGeneration, time.Now().UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockHeld
	}
	return err
}

func (s *Store) assertRunHeartbeatFenceTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	if fence, ok := NodeClaimFenceFromContext(ctx); ok {
		var held int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
	WHERE team = ? AND run_id = ? AND claimed_by = ? AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_membership_id = ? AND reservation_id = ? AND claim_generation = ?
	  AND `+nodeClaimLiveSQL("")+s.forUpdate(), string(team), runID, fence.HolderID,
			fence.Claimant.Principal, fence.Claimant.TokenPrefix, fence.MembershipID,
			fence.ReservationID, fence.ClaimGeneration, time.Now().UnixNano()).Scan(&held)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLockHeld
		}
		return err
	}
	return s.assertRunMutationFenceTx(ctx, tx, team, runID)
}

func fencedRows(result rowsAffected, fenced bool) error {
	if !fenced {
		return nil
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLockHeld
	}
	return nil
}

type rowsAffected interface {
	RowsAffected() (int64, error)
}
