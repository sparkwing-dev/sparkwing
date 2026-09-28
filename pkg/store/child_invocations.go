package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnqueueChildRun starts the child run one RunAndAwait call names, keyed by
// its invocation ID: the claim's run, node and generation plus ordinal, the
// call's position within the attempt. t becomes the child's trigger, with
// its parent taken from tok, never from t.
//
// The same invocation ID returns the same child, so a retry after a lost
// response starts nothing, while two calls with different ordinals start two
// children. A later attempt at the node reuses an earlier attempt's child at
// the same ordinal and pipeline when that child succeeded or has not
// finished. The claim must be live with no cancel requested on its run.
func (s *Store) EnqueueChildRun(ctx context.Context, tok ClaimToken, ordinal int64, t Trigger, now time.Time) (childID string, err error) {
	if ordinal < 0 || t.Pipeline == "" || t.ID == "" {
		return "", fmt.Errorf("%w: a child run needs an ID, a pipeline and a non-negative ordinal", ErrInvalidInput)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, tok.Team, tok.RunID); err != nil {
		return "", err
	}
	live, err := claimLiveTx(ctx, tx, tok.Team, tok.RunID, tok.NodeID, tok.Generation, now)
	if err != nil {
		return "", err
	}
	if !live {
		return "", ErrClaimNotLive
	}
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrClaimCancelRequested
	}
	var pipeline string
	err = tx.QueryRowContext(ctx, `SELECT child_run_id, pipeline FROM child_invocations
 WHERE team = ? AND parent_run_id = ? AND parent_node_id = ? AND claim_generation = ? AND ordinal = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, ordinal).Scan(&childID, &pipeline)
	switch {
	case err == nil && pipeline != t.Pipeline:
		return "", fmt.Errorf("%w: invocation %d already started pipeline %q", ErrClaimResultConflict, ordinal, pipeline)
	case err == nil:
		return childID, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.child_run_id FROM child_invocations c
  JOIN triggers t ON t.team = c.team AND t.id = c.child_run_id
  LEFT JOIN runs r ON r.team = c.team AND r.id = c.child_run_id
 WHERE c.team = ? AND c.parent_run_id = ? AND c.parent_node_id = ? AND c.ordinal = ? AND c.pipeline = ?
   AND c.claim_generation < ? AND t.status != ? AND COALESCE(r.status, '') NOT IN (?, ?)
 ORDER BY c.claim_generation DESC LIMIT 1`,
		string(tok.Team), tok.RunID, tok.NodeID, ordinal, t.Pipeline, tok.Generation,
		triggerStatusFailed, runStatusFailed, runStatusCancelled).Scan(&childID)
	if errors.Is(err, sql.ErrNoRows) {
		t.ParentRunID, t.ParentNodeID, t.Status = tok.RunID, tok.NodeID, ""
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		if err := createTriggerTx(ctx, tx, tok.Team, t); err != nil {
			return "", err
		}
		childID, err = t.ID, nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO child_invocations
       (team, parent_run_id, parent_node_id, claim_generation, ordinal, pipeline, child_run_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, ordinal, t.Pipeline, childID, now.UnixNano()); err != nil {
		return "", err
	}
	return childID, tx.Commit()
}
