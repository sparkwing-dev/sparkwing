package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LaunchJob names the claim one launcher Job was built for.
type LaunchJob struct {
	RunID      string `json:"run_id"`
	NodeID     string `json:"node_id"`
	Generation int64  `json:"generation"`
	// Release asks for the claim back when its pod never started: the node
	// returns to the queue unbilled with no attempt spent.
	Release bool `json:"release,omitempty"`
}

// LaunchJobState is what a launcher does with a Job.
type LaunchJobState string

const (
	// LaunchJobKeep is a Job whose claim is live and whose run goes on.
	LaunchJobKeep LaunchJobState = "keep"
	// LaunchJobDelete is a Job whose claim ended, was released, or whose run
	// is being cancelled; its pod has nothing left to do.
	LaunchJobDelete LaunchJobState = "delete"
)

// LaunchJobResult answers one [LaunchJob].
type LaunchJobResult struct {
	RunID      string         `json:"run_id"`
	NodeID     string         `json:"node_id"`
	Generation int64          `json:"generation"`
	State      LaunchJobState `json:"state"`
}

// CapacityWaitDetail is the status a node queued for a launcher shows while
// the launcher has no Cloud capacity to start it.
const CapacityWaitDetail = "waiting for Cloud capacity"

const maxCapacityWaitMarks = 100

// SyncLaunchJobs answers, for each Job the launcher holds, whether to keep
// or delete it, releasing a claim the launcher asks back when its pod never
// started. A non-empty waitReason marks up to 100 queued nodes as waiting for
// Cloud capacity, once each, with a capacity_wait event on their run.
func (s *Store) SyncLaunchJobs(ctx context.Context, launcher ClaimIdentity, jobs []LaunchJob, waitReason string, now time.Time) ([]LaunchJobResult, error) {
	out := make([]LaunchJobResult, 0, len(jobs))
	for _, j := range jobs {
		state, err := s.launchJobState(ctx, launcher, j, now)
		if err != nil {
			return nil, fmt.Errorf("job %s/%s: %w", j.RunID, j.NodeID, err)
		}
		out = append(out, LaunchJobResult{RunID: j.RunID, NodeID: j.NodeID, Generation: j.Generation, State: state})
	}
	if waitReason != "" {
		if err := s.markCapacityWait(ctx, waitReason, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) launchJobState(ctx context.Context, launcher ClaimIdentity, j LaunchJob, now time.Time) (_ LaunchJobState, err error) {
	var team string
	var started sql.NullInt64
	var billingFrom int64
	err = s.queryRow(ctx, `SELECT n.team, n.execution_started_at, n.credit_billing_from FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.run_id = ? AND n.node_id = ? AND n.claim_generation = ? AND n.claim_token_prefix = ?
   AND n.`+nodeNotDone+` AND `+nodeClaimLiveSQL("n.")+` AND r.cancel_requested_at IS NULL`,
		j.RunID, j.NodeID, j.Generation, launcher.TokenPrefix, now.UnixNano()).Scan(&team, &started, &billingFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return LaunchJobDelete, nil
	}
	if err != nil {
		return "", err
	}
	if !j.Release || started.Valid || billingFrom != 0 {
		return LaunchJobKeep, nil
	}
	released, err := s.releaseLaunchClaim(ctx, Team(team), launcher, j, now)
	if err != nil || !released {
		return LaunchJobKeep, err
	}
	return LaunchJobDelete, nil
}

// safety: only a claim whose pod never renewed it is released, so nothing was
// billed and no pipeline code ran; the reservation is refunded and the attempt
// count kept, so waiting for capacity costs the run nothing.
func (s *Store) releaseLaunchClaim(ctx context.Context, team Team, launcher ClaimIdentity, j LaunchJob, now time.Time) (_ bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, team, j.RunID); err != nil {
		return false, err
	}
	var held int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND claim_token_prefix = ?
   AND execution_started_at IS NULL AND credit_billing_from = 0 AND `+nodeNotDone+` AND `+nodeClaimLiveSQL("")+tx.forUpdate(),
		string(team), j.RunID, j.NodeID, j.Generation, launcher.TokenPrefix, now.UnixNano()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.chargeNodeTx(ctx, tx, j.RunID, j.NodeID, launcher.TokenPrefix, now, true); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET credit_charged_through = 0, status_detail = ?,
       `+endClaimSet+`
 WHERE team = ? AND run_id = ? AND node_id = ?`, CapacityWaitDetail, string(team), j.RunID, j.NodeID); err != nil {
		return false, err
	}
	if _, err := appendEventTx(ctx, tx, j.RunID, j.NodeID, "capacity_wait",
		map[string]string{"reason": "its Job could not be scheduled"}, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) markCapacityWait(ctx context.Context, reason string, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	rows, err := tx.QueryContext(ctx, `SELECT n.team, n.run_id, n.node_id FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.kind IN ('`+nodeKindPlan+`', '`+nodeKindWork+`') AND n.ready_at IS NOT NULL AND n.ready_at <= ?
   AND n.claimed_by IS NULL AND n.`+nodeNotDone+` AND n.status_detail != ?
   AND r.dispatch != '' AND r.cancel_requested_at IS NULL
 ORDER BY n.ready_at LIMIT `+fmt.Sprint(maxCapacityWaitMarks), now.UnixNano(), CapacityWaitDetail)
	if err != nil {
		return err
	}
	var waiting [][3]string
	for rows.Next() {
		var k [3]string
		if err := rows.Scan(&k[0], &k[1], &k[2]); err != nil {
			closeRowsOrLog(rows)
			return err
		}
		waiting = append(waiting, k)
	}
	closeRowsOrLog(rows)
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range waiting {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET status_detail = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND claimed_by IS NULL`, CapacityWaitDetail, k[0], k[1], k[2]); err != nil {
			return err
		}
		if _, err := appendEventTx(ctx, tx, k[1], k[2], "capacity_wait", map[string]string{"reason": reason}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
