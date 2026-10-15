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
	nodeClaimFenceKey    struct{}
	triggerClaimFenceKey struct{}
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

// safety: the fence reads the node in the mutating handle's team; a caller without a handle passes
// the run's own team, so the fence still binds the claim to the holder that owns that node.
func (s *Store) assertNodeMutationFenceTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) error {
	fence, ok := NodeClaimFenceFromContext(ctx)
	var held int
	var err error
	if ok {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
	WHERE team = ? AND run_id = ? AND node_id = ? AND claimed_by = ?
	  AND claim_principal = ? AND claim_token_prefix = ?
	  AND claim_membership_id = ? AND reservation_id = ? AND claim_generation = ?
	  AND `+nodeClaimLiveSQL("")+s.forUpdate(),
			string(team), runID, nodeID, fence.HolderID, fence.Claimant.Principal, fence.Claimant.TokenPrefix,
			fence.MembershipID, fence.ReservationID, fence.ClaimGeneration, time.Now().UnixNano()).Scan(&held)
	} else if _, triggerOK := TriggerClaimFenceFromContext(ctx); triggerOK {
		if err := s.assertRunMutationFenceTx(ctx, tx, team, runID); err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ?`+s.forUpdate(), string(team), runID, nodeID).Scan(&held)
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

// safety: a caller without a tenant handle fences the node in its run's own team, the team the
// node was written into.
func (s *Store) assertNodeMutationFenceInRunsTeamTx(ctx context.Context, tx *storeTx, runID, nodeID string) error {
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	return s.assertNodeMutationFenceTx(ctx, tx, team, runID, nodeID)
}

// safety: a node of another team's run reads as not found before the fence is
// consulted, so a mutation that matched nothing cannot pass for one that did.
func (t *Tenant) assertNodeMutationTx(ctx context.Context, tx *storeTx, runID, nodeID string) error {
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	return t.s.assertNodeMutationFenceTx(ctx, tx, t.team, runID, nodeID)
}

func (t *Tenant) execNodeMutation(ctx context.Context, runID, nodeID, query string, args ...any) (rowsAffected, bool, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := t.assertNodeMutationTx(ctx, tx, runID, nodeID); err != nil {
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

// safety: the fence reads a tenant-owned table, so it takes the mutating handle's team rather than
// matching an id across teams. A node, event or attempt path has no tenant handle, so a trigger
// holder's fence is checked in the run's own team; the fence still binds the claim to its holder.
func (s *Store) assertRunMutationFenceInRunsTeamTx(ctx context.Context, tx *storeTx, runID string) error {
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	return s.assertRunMutationFenceTx(ctx, tx, team, runID)
}

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
