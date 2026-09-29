package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	// LifetimeSecs is how long the token lives from the claim; a launcher
	// derives its Job's deadline from it, never from an expiry read against
	// its own clock.
	LifetimeSecs int64 `json:"lifetime_secs"`
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
// A node is the launcher's only when the Cloud runner image satisfies its
// selector, judged by the one evaluator every claim uses.
func (s *Store) ClaimLaunch(ctx context.Context, launcher ClaimIdentity, req LaunchClaimRequest, now time.Time) (*LaunchClaim, error) {
	if req.HolderID == "" || req.Deadline < MinLaunchLifetime || req.Deadline > MaxClaimTokenLifetime {
		return nil, fmt.Errorf("%w: a launch claim needs a holder and a deadline from %s to %s",
			ErrInvalidInput, MinLaunchLifetime, MaxClaimTokenLifetime)
	}
	coordinatorID, err := s.CoordinatorID(ctx)
	if err != nil {
		return nil, err
	}
	named := req.RunID != "" || req.NodeID != ""
	var after *launchCursor
	if !named {
		// safety: one poll reads and advances the resume point under one lock,
		// so a slower concurrent poll can never roll it back.
		s.launchResumeMu.Lock()
		defer s.launchResumeMu.Unlock()
		after = s.launchResume
	}
	wrapped := false
	for range launchMaxPages {
		page, err := s.launchCandidates(ctx, req, after, now)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			if after == nil || wrapped {
				s.resetLaunchResume(named, nil)
				return nil, nil
			}
			after, wrapped = nil, true
			continue
		}
		for _, c := range page {
			if !c.cloudRuns {
				continue
			}
			claim, err := s.claimLaunchCandidate(ctx, launcher, req, coordinatorID, c.claim, now)
			if unpaidLaunch(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if claim != nil {
				s.resetLaunchResume(named, nil)
				return claim, nil
			}
		}
		after = &page[len(page)-1].cursor
	}
	s.resetLaunchResume(named, after)
	return nil, nil
}

// safety: this bounds one poll's scan; a poll that spends it resumes the
// next one where it stopped, so a queue of unpayable nodes costs each poll a
// fixed amount and still lets every node behind it be reached in turn.
const launchMaxPages = 4

// safety: a claim that names its node scans that node alone, so it neither
// reads nor moves the queue's resume point; ClaimLaunch holds launchResumeMu
// for every other call.
func (s *Store) resetLaunchResume(named bool, c *launchCursor) {
	if !named {
		s.launchResume = c
	}
}

type launchCursor struct {
	readyAt       int64
	runID, nodeID string
}

type launchCandidate struct {
	claim     LaunchClaim
	cursor    launchCursor
	cloudRuns bool
}

// safety: a Job runs the Cloud runner image and nothing else, so it offers
// exactly the image's declared tools, the class and location the launcher's
// scope grants, and no label a pipeline could assert.
func cloudCanRun(runID, nodeID string, needsJSON []byte) bool {
	var needs []string
	if !decodeCandidateLabels(runID, nodeID, needsJSON, &needs) {
		return false
	}
	profile := match.Profile{
		Class: match.ClassCloud, Location: executorLocationCloud,
		Labels: match.ToolLabels(match.CloudTools),
	}
	return match.Evaluate(profile, match.Demand{Selector: needs}).OK()
}

// safety: the scan pages past the candidates a claim refused, and a spent
// budget resumes there, so a queue head of one team's unpaid nodes never hides
// another team's paid one.
func (s *Store) launchCandidates(ctx context.Context, req LaunchClaimRequest, after *launchCursor, now time.Time) ([]launchCandidate, error) {
	where, args := "", []any{now.UnixNano()}
	if req.RunID != "" || req.NodeID != "" {
		where, args = ` AND n.run_id = ? AND n.node_id = ?`, append(args, req.RunID, req.NodeID)
	}
	if after != nil {
		where += ` AND (n.ready_at > ? OR (n.ready_at = ? AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))))`
		args = append(args, after.readyAt, after.readyAt, after.runID, after.runID, after.nodeID)
	}
	rows, err := s.query(ctx, `SELECT n.team, n.run_id, n.node_id, n.ready_at, n.needs_labels FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.kind IN ('`+nodeKindPlan+`', '`+nodeKindWork+`') AND n.ready_at IS NOT NULL AND n.ready_at <= ?
   AND n.claimed_by IS NULL AND n.status != '`+nodeStatusDone+`'
   AND r.dispatch != '' AND r.cancel_requested_at IS NULL`+where+`
 ORDER BY n.ready_at, n.run_id, n.node_id LIMIT `+fmt.Sprint(launchScanBatch), args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []launchCandidate
	for rows.Next() {
		var c launchCandidate
		var needs []byte
		if err := rows.Scan(&c.claim.Team, &c.claim.RunID, &c.claim.NodeID, &c.cursor.readyAt, &needs); err != nil {
			return nil, err
		}
		c.cloudRuns = cloudCanRun(c.claim.RunID, c.claim.NodeID, needs)
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
	lifetime := launchDeadline(req.Deadline, time.Duration(timeoutMS)*time.Millisecond)
	c.LifetimeSecs = int64(lifetime / time.Second)
	c.Token, err = mintClaimTokenTx(ctx, tx, c.Team, c.RunID, c.NodeID, n.ClaimGeneration, c.Kind, now.Add(lifetime), now)
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

// MinLaunchLifetime is the shortest claim token a launch claim mints: a minute
// of work plus the launcher's margin for creating the Job. A shorter one would
// start a Job that dies before its pod runs.
const MinLaunchLifetime = 90 * time.Second

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
