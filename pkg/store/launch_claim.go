package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LaunchClaimRequest is what the launcher asks of [Store.ClaimLaunch].
type LaunchClaimRequest struct {
	HolderID string
	Lease    time.Duration
	// Deadline is the life of the Job the launcher builds for the claim, and so
	// the claim token's hard expiry. It is at most [MaxClaimTokenLifetime].
	Deadline time.Duration
	// RunID and NodeID name the node to claim; empty claims the oldest ready
	// node of any team's controller-dispatched run.
	RunID, NodeID string
}

// LaunchClaim is a node the launcher claimed, and the one claim token the
// node's Job hands its pod.
type LaunchClaim struct {
	Team       Team           `json:"team"`
	RunID      string         `json:"run_id"`
	NodeID     string         `json:"node_id"`
	Generation int64          `json:"generation"`
	Kind       ClaimTokenKind `json:"kind"`
	Dispatch   RepoDispatch   `json:"dispatch"`
	Class      CPUClass       `json:"class"`
	Token      string         `json:"token"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

const launchScanBatch = 16

// ClaimLaunch claims a ready node of a controller-dispatched run for the
// launcher and mints the node's claim token in the same transaction, so a
// claim never exists without the token its pod reports through. The claim
// always reserves the node's team's credits, since a token carrying
// claims.launch is metered, and a node whose team cannot pay is passed over
// for the next one. It returns nil when
// no node is ready.
//
// Only nodes that require no executor labels are the launcher's: a Job
// advertises none.
func (s *Store) ClaimLaunch(ctx context.Context, launcher ClaimIdentity, req LaunchClaimRequest, now time.Time) (*LaunchClaim, error) {
	if req.HolderID == "" || req.Deadline <= 0 || req.Deadline > MaxClaimTokenLifetime {
		return nil, fmt.Errorf("%w: a launch claim needs a holder and a deadline within %s", ErrInvalidInput, MaxClaimTokenLifetime)
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	var after *launchCursor
	for {
		page, err := s.launchCandidates(ctx, req, after, now)
		if err != nil || len(page) == 0 {
			return nil, err
		}
		for _, c := range page {
			claim, err := s.claimLaunchCandidate(ctx, launcher, req, coordinatorID, c.claim, now)
			if unpaidLaunch(err) {
				continue
			}
			if err != nil || claim != nil {
				return claim, err
			}
		}
		after = &page[len(page)-1].cursor
	}
}

type launchCursor struct {
	readyAt       int64
	runID, nodeID string
}

type launchCandidate struct {
	claim  LaunchClaim
	cursor launchCursor
}

// safety: the scan pages past every candidate the claim refused, so a queue
// head of one team's unpaid nodes never hides another team's paid one.
func (s *Store) launchCandidates(ctx context.Context, req LaunchClaimRequest, after *launchCursor, now time.Time) ([]launchCandidate, error) {
	where, args := "", []any{now.UnixNano()}
	if req.RunID != "" || req.NodeID != "" {
		where, args = ` AND n.run_id = ? AND n.node_id = ?`, append(args, req.RunID, req.NodeID)
	}
	if after != nil {
		where += ` AND (n.ready_at > ? OR (n.ready_at = ? AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))))`
		args = append(args, after.readyAt, after.readyAt, after.runID, after.runID, after.nodeID)
	}
	rows, err := s.query(ctx, `SELECT n.team, n.run_id, n.node_id, n.ready_at FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.kind IN ('`+nodeKindPlan+`', '`+nodeKindWork+`') AND n.ready_at IS NOT NULL AND n.ready_at <= ?
   AND n.claimed_by IS NULL AND n.status != '`+nodeStatusDone+`' AND n.needs_labels IS NULL
   AND r.dispatch != '' AND r.cancel_requested_at IS NULL`+where+`
 ORDER BY n.ready_at, n.run_id, n.node_id LIMIT `+fmt.Sprint(launchScanBatch), args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []launchCandidate
	for rows.Next() {
		var c launchCandidate
		if err := rows.Scan(&c.claim.Team, &c.claim.RunID, &c.claim.NodeID, &c.cursor.readyAt); err != nil {
			return nil, err
		}
		c.cursor.runID, c.cursor.nodeID = c.claim.RunID, c.claim.NodeID
		out = append(out, c)
	}
	return out, rows.Err()
}

// safety: one team's empty balance must not stall every other team's work
// behind it in the launcher's queue.
func unpaidLaunch(err error) bool {
	var credits *InsufficientCreditsError
	var limit *ComputeLimitError
	var unpriced *UnpricedCPUClassError
	return errors.As(err, &credits) || errors.As(err, &limit) || errors.As(err, &unpriced)
}

func (s *Store) claimLaunchCandidate(ctx context.Context, launcher ClaimIdentity, req LaunchClaimRequest,
	coordinatorID string, c LaunchClaim, now time.Time,
) (*LaunchClaim, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	// safety: the run row is locked before the award touches its node, which is
	// the store's order (eligibility, trigger, run, node, free tier, ledger)
	// that settle and plan accept take on the same run.
	if err := lockDispatchRunTx(ctx, tx, c.Team, c.RunID); err != nil {
		return nil, err
	}
	candidate := claimCandidate{runID: c.RunID, nodeID: c.NodeID, decision: placementDecision{reason: PlacementNone}}
	n, err := s.awardScannedNodeTx(ctx, tx, candidate, launcher, req.HolderID, coordinatorID,
		clampNodeLease(req.Lease), ClaimPlacement{}, ` AND ready_at IS NOT NULL`, oneTeam(c.Team), now)
	if err != nil || n == nil {
		return nil, err
	}
	// safety: the launcher's scope is what marks a claim as Cloud compute; a
	// billing flag on a token says who pays, not where the work runs.
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET executor_kind = 'kubernetes', executor_id = ?,
       executor_location = ? WHERE team = ? AND run_id = ? AND node_id = ?`,
		req.HolderID, executorLocationCloud, string(c.Team), c.RunID, c.NodeID); err != nil {
		return nil, err
	}
	var kind, dispatch string
	var timeoutMS int64
	if err := tx.QueryRowContext(ctx, `SELECT n.kind, r.dispatch, n.timeout_ms FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.team = ? AND n.run_id = ? AND n.node_id = ?`, string(c.Team), c.RunID, c.NodeID).Scan(&kind, &dispatch, &timeoutMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("node", c.RunID+"/"+c.NodeID)
		}
		return nil, err
	}
	c.Kind = ClaimTokenWork
	if kind == nodeKindPlan {
		c.Kind = ClaimTokenPlan
	}
	c.ExpiresAt = now.Add(launchDeadline(req.Deadline, time.Duration(timeoutMS)*time.Millisecond))
	c.Token, err = mintClaimTokenTx(ctx, tx, c.Team, c.RunID, c.NodeID, n.ClaimGeneration, c.Kind, c.ExpiresAt, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.Generation, c.Dispatch = n.ClaimGeneration, RepoDispatch(dispatch)
	c.Class = CPUClass{Cores: n.CreditCPUClassCores, MemoryBytes: n.CreditCPUClassMemoryBytes}
	return &c, nil
}

// LaunchDeadlineSlack is how long past a node's own timeout its claim, and so
// its Job, lives, so the pod records the timeout before the Job is killed.
const LaunchDeadlineSlack = 10 * time.Minute

// safety: the claim token, whose expiry is the Job's deadline, runs from the
// claim, so a node's declared timeout shortens it and never lengthens it past
// the launcher's cap.
func launchDeadline(limit, timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return limit
	}
	return min(limit, timeout+LaunchDeadlineSlack)
}
