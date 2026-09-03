package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Bounce outcomes: how a requested bounce ended, recorded on the row
// when the runner consumes it.
const (
	// BounceBounced means the kill landed on a live process and the
	// node was re-run.
	BounceBounced = "bounced"
	// BounceMissed means the node reached its terminal row before the
	// kill did, so the row won and nothing was re-run.
	BounceMissed = "missed"
)

// ErrNodeNotRunning reports a bounce aimed at a node with no process
// to kill. Wrapped with the node's actual status, which is the detail
// the operator needs.
var ErrNodeNotRunning = errors.New("node is not running")

// ErrRunNotLive reports a bounce aimed at a run that has already
// finished. Nothing is executing it, so there is nothing to restart.
var ErrRunNotLive = errors.New("run is not live")

// NodeBounce is one operator request to restart a running node's
// process in place, and the record of what became of that request.
//
// It is an intent rather than an action because the operator's CLI is
// not the process holding the node's child: the runner supervising it
// is, and it polls for this row on the same loop that heartbeats the
// node. The row is also how that runner tells an operator's kill apart
// from a crash -- without it, a node process that dies without a
// terminal row is a failure, which is exactly the verdict a bounce
// must not produce.
type NodeBounce struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
	// Seq counts requests per node, so repeated bounces are separate
	// rows rather than one row overwritten: an operator who bounced a
	// wedged node three times asked three times, and the history says
	// so.
	Seq         int64      `json:"seq"`
	RequestedAt time.Time  `json:"requested_at"`
	RequestedBy string     `json:"requested_by,omitempty"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
}

// RequestNodeBounce records the intent to bounce one running node and
// returns the row it wrote.
//
// It refuses the two requests that could only confuse: a run that has
// already finished, and a node that is not currently running. Both are
// checked here rather than in the CLI because the controller serves
// the same call over HTTP, and a guard that lives in one caller is not
// a guard.
//
// The check is a snapshot, not a lock. A node that finishes between
// this call and the kill is the race the consuming runner resolves:
// the terminal row wins and the request is consumed as BounceMissed.
func (s *Store) RequestNodeBounce(ctx context.Context, runID, nodeID, requestedBy string) (*NodeBounce, error) {
	var runStatus string
	switch err := s.queryRow(ctx,
		`SELECT status FROM runs WHERE id = ?`, runID).Scan(&runStatus); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("run %s: %w", runID, ErrNotFound)
	case err != nil:
		return nil, err
	}
	if isTerminalRunStatus(runStatus) {
		return nil, fmt.Errorf("run %s already finished (%s): %w", runID, runStatus, ErrRunNotLive)
	}

	var nodeStatus string
	switch err := s.queryRow(ctx,
		`SELECT status FROM nodes WHERE run_id = ? AND node_id = ?`,
		runID, nodeID).Scan(&nodeStatus); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("node %s in run %s: %w", nodeID, runID, ErrNotFound)
	case err != nil:
		return nil, err
	}
	if nodeStatus != nodeStatusRunning {
		return nil, fmt.Errorf("node %s in run %s is %s: %w",
			nodeID, runID, nodeStatus, ErrNodeNotRunning)
	}

	// safety: allocate the request number and insert in one transaction or
	// concurrent bounces can choose the same primary key. The transaction is
	// not enough on its own under Postgres' READ COMMITTED, so the node row
	// is held across the read and the insert.
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var locked string
	if err := tx.QueryRowContext(ctx,
		`SELECT node_id FROM nodes WHERE run_id = ? AND node_id = ?`+tx.forUpdate(),
		runID, nodeID).Scan(&locked); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var seq int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(seq), 0) + 1
  FROM node_bounces
 WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&seq); err != nil {
		return nil, fmt.Errorf("assign next bounce seq: %w", err)
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO node_bounces (run_id, node_id, seq, requested_at, requested_by)
VALUES (?,?,?,?,?)`, runID, nodeID, seq, now.UnixNano(), requestedBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &NodeBounce{
		RunID:       runID,
		NodeID:      nodeID,
		Seq:         seq,
		RequestedAt: now,
		RequestedBy: requestedBy,
	}, nil
}

// PendingNodeBounce returns the oldest unconsumed bounce request for a
// node, or (nil, nil) when the node has none. A runner polls it on
// every supervision tick, so "none" is the common answer and is not an
// error.
func (s *Store) PendingNodeBounce(ctx context.Context, runID, nodeID string) (*NodeBounce, error) {
	row := s.queryRow(ctx, `
SELECT run_id, node_id, seq, requested_at, requested_by, consumed_at, outcome
  FROM node_bounces
 WHERE run_id = ? AND node_id = ? AND consumed_at IS NULL
 ORDER BY seq
 LIMIT 1`, runID, nodeID)
	b, err := scanNodeBounce(row)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return b, err
}

// ConsumeNodeBounce closes one request with the outcome it produced.
//
// It is idempotent: a request already consumed reports success without
// rewriting the outcome, because the runner that consumed it is the
// only party that could consume it again and the first verdict is the
// true one. A seq that names no row is ErrNotFound -- that is a caller
// bug, not a replay.
func (s *Store) ConsumeNodeBounce(ctx context.Context, runID, nodeID string, seq int64, outcome string) error {
	res, err := s.exec(ctx, `
UPDATE node_bounces
   SET consumed_at = ?, outcome = ?
 WHERE run_id = ? AND node_id = ? AND seq = ? AND consumed_at IS NULL`,
		time.Now().UnixNano(), outcome, runID, nodeID, seq)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := s.queryRow(ctx, `
SELECT COUNT(*) FROM node_bounces
 WHERE run_id = ? AND node_id = ? AND seq = ?`, runID, nodeID, seq).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("bounce %s/%s#%d: %w", runID, nodeID, seq, ErrNotFound)
	}
	return nil
}

// ListNodeBounces returns every bounce request recorded for a run,
// oldest first. It serves inspection -- a node's history, and tests --
// rather than the runner's poll.
func (s *Store) ListNodeBounces(ctx context.Context, runID string) ([]*NodeBounce, error) {
	rows, err := s.query(ctx, `
SELECT run_id, node_id, seq, requested_at, requested_by, consumed_at, outcome
  FROM node_bounces
 WHERE run_id = ?
 ORDER BY requested_at, seq`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*NodeBounce
	for rows.Next() {
		b, err := scanNodeBounce(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanNodeBounce(rs rowScanner) (*NodeBounce, error) {
	var b NodeBounce
	var requestedNS int64
	var consumedNS sql.NullInt64
	err := rs.Scan(&b.RunID, &b.NodeID, &b.Seq, &requestedNS, &b.RequestedBy,
		&consumedNS, &b.Outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.RequestedAt = time.Unix(0, requestedNS)
	if consumedNS.Valid {
		t := time.Unix(0, consumedNS.Int64)
		b.ConsumedAt = &t
	}
	return &b, nil
}

func isTerminalRunStatus(status string) bool {
	return status == "success" || status == runStatusFailed || status == runStatusCancelled
}
